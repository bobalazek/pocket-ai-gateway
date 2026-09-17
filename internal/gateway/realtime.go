package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"golang.org/x/net/websocket"
)

const (
	realtimeFrameLimit   = 16 << 20
	realtimeSessionLimit = 10 * time.Minute
)

type realtimeMessage struct {
	payload     []byte
	payloadType byte
}

var realtimeCodec = websocket.Codec{
	Marshal: func(value any) ([]byte, byte, error) {
		message, ok := value.(realtimeMessage)
		if !ok {
			return nil, websocket.UnknownFrame, websocket.ErrNotSupported
		}
		return message.payload, message.payloadType, nil
	},
	Unmarshal: func(payload []byte, payloadType byte, value any) error {
		message, ok := value.(*realtimeMessage)
		if !ok {
			return websocket.ErrNotSupported
		}
		message.payload = append(message.payload[:0], payload...)
		message.payloadType = payloadType
		return nil
	},
}

type realtimeUsage struct {
	input, output int64
	known         bool
	terminal      bool
	providerError bool
	firstByte     time.Time
}

func (capture *realtimeUsage) observe(message realtimeMessage) {
	if capture.firstByte.IsZero() {
		capture.firstByte = time.Now()
	}
	if message.payloadType != websocket.TextFrame {
		return
	}
	var envelope map[string]any
	if json.Unmarshal(message.payload, &envelope) != nil {
		return
	}
	switch stringValue(envelope["type"]) {
	case "response.done":
		capture.terminal = true
		parsed := parseUsageDetails("openai", message.payload)
		if parsed.inputTokens != nil && parsed.outputTokens != nil {
			capture.input += *parsed.inputTokens
			capture.output += *parsed.outputTokens
			capture.known = true
		}
	case "session.closed":
		capture.terminal = true
	case "error":
		capture.providerError = true
	}
}

func (handler *Handler) realtime(response http.ResponseWriter, request *http.Request) {
	if !isWebSocketUpgrade(request) {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "A WebSocket upgrade is required")
		return
	}
	if _, ok := response.(http.Hijacker); !ok {
		handler.writeError(response, "openai", http.StatusNotImplemented, "unsupported_transport", "WebSocket upgrades require HTTP/1.1")
		return
	}
	principal, ok := handler.authenticate(response, request, "openai")
	if !ok {
		return
	}
	publicModel := strings.TrimSpace(request.URL.Query().Get("model"))
	if publicModel == "" || len(publicModel) > 200 {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	quoteAt, err := handler.usage.QuoteTime(request.Context())
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "A current price quote could not be created")
		return
	}
	plan, err := handler.providers.Route(request.Context(), publicModel, providers.RouteOptions{
		Operation: "realtime", Streaming: true, QuoteAt: quoteAt, Seed: principal.KeyID + "\x00" + publicModel,
		AllowsConnection: func(connectionID string) bool { return principal.Allows("realtime:connect", publicModel, connectionID) },
		Eligibility: func(target providers.Target) (bool, string) {
			if target.Adapter != "openai" {
				return false, "translation_unsupported"
			}
			if !providers.SupportsScope(target.Capabilities, "realtime:connect") || !providers.SupportsScope(target.UpstreamCapabilities, "realtime:connect") {
				return false, "unsupported_capability"
			}
			if !providers.PresetSupports(target.Preset, "realtime") {
				return false, "preset_operation_unsupported"
			}
			return true, ""
		},
	})
	if err != nil {
		handler.writeError(response, "openai", http.StatusNotFound, "model_not_found", "Model is unavailable")
		return
	}
	rejected, _ := json.Marshal(plan.Rejected)
	requestID := ""
	for index, routeTarget := range plan.Targets {
		target := routeTarget.Target()
		quotedPriceVersionID := routeTarget.PriceVersionID()
		admission, admitErr := handler.usage.Admit(request.Context(), usage.AdmissionInput{
			RequestID: requestID, KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicModel,
			UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID,
			ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision,
			Operation: "realtime", TargetOperation: "realtime", Scope: "realtime:connect", Dialect: "openai", TargetDialect: target.Adapter,
			SelectionReason: plan.SelectionReason, RejectedCandidatesJSON: string(rejected), RequiredPriceVersionID: quotedPriceVersionID,
			PriceQuoteAt: quoteAt, QuotedPriceVersionID: &quotedPriceVersionID, RequireFreePrice: plan.FreeOnly,
			BatchItems: 1, EnforceInputBound: true, EnforceOutputBound: true,
		})
		if admitErr != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			handler.writeAdmissionError(response, "openai", admitErr)
			return
		}
		requestID = admission.RequestID
		releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
		if !current {
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Provider configuration changed before dispatch")
			return
		}
		if err = handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
			releaseDispatch()
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Request could not be dispatched")
			return
		}
		started := time.Now()
		upstream, dialErr := dialRealtime(request.Context(), target)
		if dialErr != nil {
			releaseDispatch()
			final := index+1 == len(plan.Targets)
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: final})
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "realtime", true, false, 0, time.Since(started))
			if final {
				handler.writeError(response, "openai", http.StatusBadGateway, "upstream_error", "Provider WebSocket connection failed")
				return
			}
			continue
		}
		response.Header().Set(pocketAIRequestIDHeader, requestID)
		handler.serveRealtime(response, request, upstream, target, admission, requestID, started, releaseDispatch)
		return
	}
}

func (handler *Handler) serveRealtime(response http.ResponseWriter, request *http.Request, upstream *websocket.Conn, target providers.Target, admission usage.Admission, requestID string, started time.Time, releaseDispatch func()) {
	defer releaseDispatch()
	defer upstream.Close()
	upgraded := false
	server := websocket.Server{
		Config: websocket.Config{Header: http.Header{pocketAIRequestIDHeader: []string{requestID}}},
		Handshake: func(config *websocket.Config, _ *http.Request) error {
			config.Protocol = append([]string(nil), upstream.Config().Protocol...)
			return nil
		},
		Handler: func(client *websocket.Conn) {
			upgraded = true
			defer client.Close()
			deadline := time.Now().Add(realtimeSessionLimit)
			_ = client.SetDeadline(deadline)
			_ = upstream.SetDeadline(deadline)
			client.MaxPayloadBytes = realtimeFrameLimit
			upstream.MaxPayloadBytes = realtimeFrameLimit
			capture := realtimeUsage{}
			results := make(chan error, 2)
			go func() { results <- relayRealtime(client, upstream, nil) }()
			go func() { results <- relayRealtime(upstream, client, capture.observe) }()
			firstErr := <-results
			_ = client.Close()
			_ = upstream.Close()
			secondErr := <-results
			success := capture.terminal && !capture.providerError && benignWebSocketError(firstErr) && benignWebSocketError(secondErr)
			state, usageStatus := "interrupted_unknown", "unknown"
			var inputTokens, outputTokens *int64
			if capture.providerError {
				state = "failed"
			} else if success {
				state = "succeeded"
				if capture.known {
					usageStatus, inputTokens, outputTokens = "provider_reported", &capture.input, &capture.output
				}
			}
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: usageStatus, InputTokens: inputTokens, OutputTokens: outputTokens, FinalRequest: true})
			firstByte := time.Duration(0)
			if !capture.firstByte.IsZero() {
				firstByte = capture.firstByte.Sub(started)
			}
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "realtime", true, success, firstByte, time.Since(started))
		},
	}
	server.ServeHTTP(response, request)
	if !upgraded {
		_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: true})
	}
}

func relayRealtime(source, destination *websocket.Conn, observe func(realtimeMessage)) error {
	for {
		message := realtimeMessage{}
		if err := realtimeCodec.Receive(source, &message); err != nil {
			return err
		}
		if observe != nil {
			observe(message)
		}
		if err := realtimeCodec.Send(destination, message); err != nil {
			return err
		}
	}
}

func dialRealtime(ctx context.Context, target providers.Target) (*websocket.Conn, error) {
	endpoint, err := joinURL(target.BaseURL, "realtime")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return nil, errors.New("provider WebSocket URL is invalid")
	}
	query := parsed.Query()
	query.Set("model", target.UpstreamID)
	parsed.RawQuery = query.Encode()
	originScheme := "http"
	if parsed.Scheme == "wss" {
		originScheme = "https"
	}
	config, err := websocket.NewConfig(parsed.String(), originScheme+"://"+parsed.Host)
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+target.Credential)
	dialContext, cancel := context.WithTimeout(ctx, time.Duration(target.TimeoutMS)*time.Millisecond)
	defer cancel()
	connection, err := dialWebSocketTransport(dialContext, parsed, target.AllowPrivateNetwork)
	if err != nil {
		return nil, err
	}
	if deadline, ok := dialContext.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	client, err := websocket.NewClient(config, connection)
	if err != nil {
		connection.Close()
		return nil, err
	}
	_ = client.SetDeadline(time.Time{})
	return client, nil
}

func dialWebSocketTransport(ctx context.Context, endpoint *url.URL, allowPrivate bool) (net.Conn, error) {
	host := endpoint.Hostname()
	port := endpoint.Port()
	if port == "" {
		if endpoint.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, ip := range ips {
		if !allowPrivate && (!ip.IsGlobalUnicast() || ip.IsPrivate()) {
			continue
		}
		raw, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if dialErr != nil {
			failures = append(failures, dialErr)
			continue
		}
		if endpoint.Scheme != "wss" {
			return raw, nil
		}
		secured := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
		if handshakeErr := secured.HandshakeContext(ctx); handshakeErr == nil {
			return secured, nil
		} else {
			failures = append(failures, handshakeErr)
			_ = raw.Close()
		}
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return nil, errors.New("provider destination is not allowed")
}

func isWebSocketUpgrade(request *http.Request) bool {
	return headerContainsToken(request.Header, "Connection", "upgrade") && headerContainsToken(request.Header, "Upgrade", "websocket")
}

func headerContainsToken(header http.Header, name, wanted string) bool {
	for _, value := range header.Values(name) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), wanted) {
				return true
			}
		}
	}
	return false
}

func benignWebSocketError(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && !netErr.Timeout() || strings.Contains(err.Error(), "use of closed network connection")
}
