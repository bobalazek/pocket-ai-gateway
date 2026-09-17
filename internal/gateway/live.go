package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"golang.org/x/net/websocket"
)

const liveStartTimeout = 10 * time.Second

func (handler *Handler) live(response http.ResponseWriter, request *http.Request) {
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
	quoteAt, err := handler.usage.QuoteTime(request.Context())
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "A current price quote could not be created")
		return
	}
	websocket.Server{Handler: func(client *websocket.Conn) {
		defer client.Close()
		client.MaxPayloadBytes = realtimeFrameLimit
		_ = client.SetReadDeadline(time.Now().Add(liveStartTimeout))
		start := realtimeMessage{}
		if err := realtimeCodec.Receive(client, &start); err != nil {
			return
		}
		publicModel, err := liveStartModel(start)
		if err != nil {
			sendLiveError(client, "invalid_request", err.Error())
			return
		}
		_ = client.SetReadDeadline(time.Time{})
		handler.serveLiveClient(request, client, start, publicModel, principal, quoteAt)
	}}.ServeHTTP(response, request)
}

func (handler *Handler) serveLiveClient(request *http.Request, client *websocket.Conn, start realtimeMessage, publicModel string, principal keys.Principal, quoteAt int64) {
	plan, err := handler.providers.Route(request.Context(), publicModel, providers.RouteOptions{
		Operation: "live", Streaming: true, QuoteAt: quoteAt, Seed: principal.KeyID + "\x00" + publicModel,
		AllowsConnection: func(connectionID string) bool { return principal.Allows("realtime:connect", publicModel, connectionID) },
		Eligibility: func(target providers.Target) (bool, string) {
			if target.Adapter != "openai" {
				return false, "translation_unsupported"
			}
			if !providers.SupportsScope(target.Capabilities, "realtime:connect") || !providers.SupportsScope(target.UpstreamCapabilities, "realtime:connect") {
				return false, "unsupported_capability"
			}
			if !providers.PresetSupports(target.Preset, "live") {
				return false, "preset_operation_unsupported"
			}
			return true, ""
		},
	})
	if err != nil {
		sendLiveError(client, "model_not_found", "Model is unavailable")
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
			Operation: "live", TargetOperation: "live", Scope: "realtime:connect", Dialect: "openai", TargetDialect: target.Adapter,
			SelectionReason: plan.SelectionReason, RejectedCandidatesJSON: string(rejected), RequiredPriceVersionID: quotedPriceVersionID,
			PriceQuoteAt: quoteAt, QuotedPriceVersionID: &quotedPriceVersionID, RequireFreePrice: plan.FreeOnly,
			BatchItems: 1, EnforceInputBound: true, EnforceOutputBound: true,
		})
		if admitErr != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			sendLiveError(client, "admission_rejected", "Session admission was rejected")
			return
		}
		requestID = admission.RequestID
		releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
		if !current {
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			sendLiveError(client, "gateway_unavailable", "Provider configuration changed before dispatch")
			return
		}
		if err = handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
			releaseDispatch()
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			sendLiveError(client, "gateway_unavailable", "Session could not be dispatched")
			return
		}
		started := time.Now()
		upstream, dialErr := dialProviderWebSocket(request.Context(), target, "live", nil)
		if dialErr != nil {
			releaseDispatch()
			final := index+1 == len(plan.Targets)
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: final})
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "live", true, false, 0, time.Since(started))
			if final {
				sendLiveError(client, "upstream_error", "Provider WebSocket connection failed")
				return
			}
			continue
		}
		rewritten, rewriteErr := rewriteLiveSessionModel(start, target.UpstreamID)
		if rewriteErr == nil {
			rewriteErr = realtimeCodec.Send(upstream, rewritten)
		}
		if rewriteErr != nil {
			releaseDispatch()
			_ = upstream.Close()
			_ = handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: true})
			sendLiveError(client, "upstream_error", "Provider session start failed")
			return
		}
		handler.relayLiveSession(request, client, upstream, target, admission, publicModel, started, releaseDispatch)
		return
	}
}

func (handler *Handler) relayLiveSession(request *http.Request, client, upstream *websocket.Conn, target providers.Target, admission usage.Admission, publicModel string, started time.Time, releaseDispatch func()) {
	defer releaseDispatch()
	defer upstream.Close()
	deadline := time.Now().Add(realtimeSessionLimit)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	client.MaxPayloadBytes = realtimeFrameLimit
	upstream.MaxPayloadBytes = realtimeFrameLimit
	capture := realtimeUsage{}
	results := make(chan error, 2)
	go func() { results <- relayRealtime(client, upstream, nil) }()
	go func() { results <- relayLiveUpstream(upstream, client, publicModel, capture.observe) }()
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
	_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, "live", true, success, firstByte, time.Since(started))
}

func liveStartModel(message realtimeMessage) (string, error) {
	if message.payloadType != websocket.TextFrame {
		return "", errors.New("session.start must be a text event")
	}
	var envelope struct {
		Type    string `json:"type"`
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if json.Unmarshal(message.payload, &envelope) != nil || envelope.Type != "session.start" {
		return "", errors.New("the first event must be session.start")
	}
	model := strings.TrimSpace(envelope.Session.Model)
	if model == "" || len(model) > 200 {
		return "", errors.New("session.model is required")
	}
	return model, nil
}

func rewriteLiveSessionModel(message realtimeMessage, model string) (realtimeMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(message.payload, &envelope) != nil {
		return realtimeMessage{}, errors.New("live event is invalid")
	}
	var session map[string]json.RawMessage
	if json.Unmarshal(envelope["session"], &session) != nil || session == nil {
		return realtimeMessage{}, errors.New("live event session is invalid")
	}
	session["model"], _ = json.Marshal(model)
	envelope["session"], _ = json.Marshal(session)
	payload, err := json.Marshal(envelope)
	return realtimeMessage{payload: payload, payloadType: message.payloadType}, err
}

func relayLiveUpstream(source, destination *websocket.Conn, publicModel string, observe func(realtimeMessage)) error {
	for {
		message := realtimeMessage{}
		if err := realtimeCodec.Receive(source, &message); err != nil {
			return err
		}
		if observe != nil {
			observe(message)
		}
		if normalized, err := rewriteLiveSessionModel(message, publicModel); err == nil {
			message = normalized
		}
		if err := realtimeCodec.Send(destination, message); err != nil {
			return err
		}
	}
}

func sendLiveError(connection *websocket.Conn, code, message string) {
	payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "gateway_error", "code": code, "message": message}})
	_ = realtimeCodec.Send(connection, realtimeMessage{payload: payload, payloadType: websocket.TextFrame})
}
