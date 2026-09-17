package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"golang.org/x/net/websocket"
)

const geminiLivePath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

type geminiLiveUsage struct {
	input, output int64
	known         bool
	started       bool
	providerError bool
	firstByte     time.Time
}

func (capture *geminiLiveUsage) observe(message realtimeMessage) {
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
	if _, ok := envelope["setupComplete"]; ok {
		capture.started = true
	}
	if _, ok := envelope["error"]; ok {
		capture.providerError = true
	}
	parsed := parseUsageDetails("gemini", message.payload)
	if parsed.inputTokens != nil && parsed.outputTokens != nil {
		capture.input, capture.output = *parsed.inputTokens, *parsed.outputTokens
		capture.known = true
	}
}

func (handler *Handler) geminiLive(response http.ResponseWriter, request *http.Request) {
	if !isWebSocketUpgrade(request) {
		handler.writeError(response, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "A WebSocket upgrade is required")
		return
	}
	if _, ok := response.(http.Hijacker); !ok {
		handler.writeError(response, "gemini", http.StatusNotImplemented, "UNIMPLEMENTED", "WebSocket upgrades require HTTP/1.1")
		return
	}
	principal, ok := handler.authenticate(response, request, "gemini")
	if !ok {
		return
	}
	quoteAt, err := handler.usage.QuoteTime(request.Context())
	if err != nil {
		handler.writeError(response, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "A current price quote could not be created")
		return
	}
	websocket.Server{Handler: func(client *websocket.Conn) {
		defer client.Close()
		client.MaxPayloadBytes = realtimeFrameLimit
		_ = client.SetReadDeadline(time.Now().Add(liveStartTimeout))
		setup := realtimeMessage{}
		if err := realtimeCodec.Receive(client, &setup); err != nil {
			return
		}
		publicModel, err := geminiLiveSetupModel(setup)
		if err != nil {
			sendGeminiLiveError(client, "INVALID_ARGUMENT", err.Error())
			return
		}
		_ = client.SetReadDeadline(time.Time{})
		handler.serveGeminiLiveClient(request, client, setup, publicModel, principal, quoteAt)
	}}.ServeHTTP(response, request)
}

func (handler *Handler) serveGeminiLiveClient(request *http.Request, client *websocket.Conn, setup realtimeMessage, publicModel string, principal keys.Principal, quoteAt int64) {
	plan, err := handler.providers.Route(request.Context(), publicModel, providers.RouteOptions{
		Operation: "BidiGenerateContent", Streaming: true, QuoteAt: quoteAt, Seed: principal.KeyID + "\x00" + publicModel,
		AllowsConnection: func(connectionID string) bool { return principal.Allows("realtime:connect", publicModel, connectionID) },
		Eligibility: func(target providers.Target) (bool, string) {
			if target.Adapter != "gemini" {
				return false, "translation_unsupported"
			}
			if !providers.SupportsScope(target.Capabilities, "realtime:connect") || !providers.SupportsScope(target.UpstreamCapabilities, "realtime:connect") {
				return false, "unsupported_capability"
			}
			if !providers.PresetSupports(target.Preset, "BidiGenerateContent") {
				return false, "preset_operation_unsupported"
			}
			return true, ""
		},
	})
	if err != nil {
		sendGeminiLiveError(client, "NOT_FOUND", "Model is unavailable")
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
			Operation: "BidiGenerateContent", TargetOperation: "BidiGenerateContent", Scope: "realtime:connect", Dialect: "gemini", TargetDialect: target.Adapter,
			SelectionReason: plan.SelectionReason, RejectedCandidatesJSON: string(rejected), RequiredPriceVersionID: quotedPriceVersionID,
			PriceQuoteAt: quoteAt, QuotedPriceVersionID: &quotedPriceVersionID, RequireFreePrice: plan.FreeOnly,
			BatchItems: 1, EnforceInputBound: true, EnforceOutputBound: true,
		})
		if admitErr != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			sendGeminiLiveError(client, "RESOURCE_EXHAUSTED", "Session admission was rejected")
			return
		}
		requestID = admission.RequestID
		releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
		if !current {
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			sendGeminiLiveError(client, "UNAVAILABLE", "Provider configuration changed before dispatch")
			return
		}
		if err = handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
			releaseDispatch()
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			sendGeminiLiveError(client, "UNAVAILABLE", "Session could not be dispatched")
			return
		}
		started := time.Now()
		upstream, dialErr := dialGeminiLive(request.Context(), target)
		if dialErr != nil {
			releaseDispatch()
			final := index+1 == len(plan.Targets)
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: final})
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "BidiGenerateContent", true, false, 0, time.Since(started))
			if final {
				sendGeminiLiveError(client, "UNAVAILABLE", "Provider WebSocket connection failed")
				return
			}
			continue
		}
		rewritten, rewriteErr := rewriteGeminiLiveSetupModel(setup, target.UpstreamID)
		if rewriteErr == nil {
			rewriteErr = realtimeCodec.Send(upstream, rewritten)
		}
		if rewriteErr != nil {
			releaseDispatch()
			_ = upstream.Close()
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: true})
			sendGeminiLiveError(client, "UNAVAILABLE", "Provider session setup failed")
			return
		}
		handler.relayGeminiLiveSession(request, client, upstream, target, admission, started, releaseDispatch)
		return
	}
}

func (handler *Handler) relayGeminiLiveSession(request *http.Request, client, upstream *websocket.Conn, target providers.Target, admission usage.Admission, started time.Time, releaseDispatch func()) {
	defer releaseDispatch()
	defer upstream.Close()
	deadline := time.Now().Add(realtimeSessionLimit)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	client.MaxPayloadBytes = realtimeFrameLimit
	upstream.MaxPayloadBytes = realtimeFrameLimit
	capture := geminiLiveUsage{}
	results := make(chan error, 2)
	go func() { results <- relayRealtime(client, upstream, nil) }()
	go func() { results <- relayRealtime(upstream, client, capture.observe) }()
	firstErr := <-results
	_ = client.Close()
	_ = upstream.Close()
	secondErr := <-results
	success := capture.started && !capture.providerError && benignWebSocketError(firstErr) && benignWebSocketError(secondErr)
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
	_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "BidiGenerateContent", true, success, firstByte, time.Since(started))
}

func geminiLiveSetupModel(message realtimeMessage) (string, error) {
	if message.payloadType != websocket.TextFrame {
		return "", errors.New("setup must be a text message")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(message.payload, &object) != nil || len(object) != 1 || len(object["setup"]) == 0 {
		return "", errors.New("the first message must contain only setup")
	}
	var envelope struct {
		Setup struct {
			Model string `json:"model"`
		} `json:"setup"`
	}
	if json.Unmarshal(message.payload, &envelope) != nil {
		return "", errors.New("the first message must contain only setup")
	}
	model := strings.TrimSpace(envelope.Setup.Model)
	if !strings.HasPrefix(model, "models/") || len(model) <= len("models/") || len(model) > 207 {
		return "", errors.New("setup.model must use models/{model}")
	}
	return strings.TrimPrefix(model, "models/"), nil
}

func rewriteGeminiLiveSetupModel(message realtimeMessage, model string) (realtimeMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(message.payload, &envelope) != nil {
		return realtimeMessage{}, errors.New("gemini live setup is invalid")
	}
	var setup map[string]json.RawMessage
	if json.Unmarshal(envelope["setup"], &setup) != nil || setup == nil {
		return realtimeMessage{}, errors.New("gemini live setup is invalid")
	}
	setup["model"], _ = json.Marshal("models/" + model)
	envelope["setup"], _ = json.Marshal(setup)
	payload, err := json.Marshal(envelope)
	return realtimeMessage{payload: payload, payloadType: message.payloadType}, err
}

func dialGeminiLive(ctx context.Context, target providers.Target) (*websocket.Conn, error) {
	endpoint, err := url.Parse(target.BaseURL)
	if err != nil {
		return nil, err
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	default:
		return nil, errors.New("provider WebSocket URL is invalid")
	}
	endpoint.Path, endpoint.RawPath, endpoint.RawQuery = geminiLivePath, "", ""
	query := endpoint.Query()
	query.Set("key", target.Credential)
	endpoint.RawQuery = query.Encode()
	return dialWebSocketURL(ctx, target, endpoint, make(http.Header))
}

func sendGeminiLiveError(connection *websocket.Conn, code, message string) {
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	_ = realtimeCodec.Send(connection, realtimeMessage{payload: payload, payloadType: websocket.TextFrame})
}
