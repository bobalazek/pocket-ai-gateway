package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func (handler *Handler) writeAdmissionError(w http.ResponseWriter, dialect string, err error) {
	var denial *usage.Denial
	if errors.As(err, &denial) {
		if denial.RetryAfterSecond != nil {
			w.Header().Set("Retry-After", strconv.FormatInt(*denial.RetryAfterSecond, 10))
		}
		handler.writeError(w, dialect, http.StatusTooManyRequests, "rate_limit_exceeded", denial.Reason)
		return
	}
	handler.writeError(w, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Request could not be admitted")
}
func (handler *Handler) writeError(w http.ResponseWriter, dialect string, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	switch dialect {
	case "anthropic":
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": code, "message": message}, "request_id": nil})
	case "gemini":
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": message, "status": geminiErrorStatus(status)}})
	case "systemone":
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": map[string]string{"error_type": code, "message": message}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}})
	}
}

func geminiErrorStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusInternalServerError:
		return "INTERNAL"
	default:
		return "UNAVAILABLE"
	}
}
func upstreamClientStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusConflict || status == http.StatusUnprocessableEntity || status == http.StatusTooManyRequests
}

// writeUpstreamClientError keeps a provider's client-error status so SDKs do not retry a request
// that cannot succeed. Provider credential and server failures remain gateway 502 errors.
func (handler *Handler) writeUpstreamClientError(w http.ResponseWriter, dialect string, status int, raw []byte) bool {
	if !upstreamClientStatus(status) || upstreamCredentialError(raw) {
		return false
	}
	if raw != nil && writeNativeUpstreamError(w, dialect, status, raw) {
		return true
	}
	code := "upstream_rejected"
	if dialect == "systemone" {
		code = systemOneErrorType(status)
	}
	if dialect == "anthropic" {
		code = "invalid_request_error"
		if status == http.StatusTooManyRequests {
			code = "rate_limit_error"
		}
	}
	handler.writeError(w, dialect, status, code, "Provider rejected the request")
	return true
}

// upstreamCredentialError detects Gemini's 400 response to an invalid provider key, which is an
// operator configuration fault rather than a problem with the caller's request.
func upstreamCredentialError(raw []byte) bool {
	return bytes.Contains(raw, []byte("API_KEY_INVALID")) || bytes.Contains(bytes.ToLower(raw), []byte("api key not valid"))
}

func writeNativeUpstreamError(w http.ResponseWriter, dialect string, status int, raw []byte) bool {
	if dialect == "systemone" {
		kind, message, ok := systemOneUpstreamError(raw, status)
		if !ok || !upstreamClientStatus(status) {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": map[string]string{"error_type": kind, "message": message}})
		return true
	}
	var envelope struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if !upstreamClientStatus(status) || json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	var body any
	switch dialect {
	case "openai", "responses", "responses_compact":
		safe := map[string]any{}
		for _, name := range []string{"message", "type", "code", "param"} {
			value, exists := envelope.Error[name]
			if !exists {
				continue
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				safe[name] = nil
				continue
			}
			text, ok := upstreamErrorText(value)
			if !ok {
				return false
			}
			safe[name] = text
		}
		if message, _ := safe["message"].(string); message == "" {
			return false
		}
		body = map[string]any{"error": safe}
	case "anthropic":
		message, messageOK := upstreamErrorText(envelope.Error["message"])
		kind, kindOK := upstreamErrorText(envelope.Error["type"])
		if !messageOK || !kindOK || message == "" || kind == "" {
			return false
		}
		body = map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}, "request_id": nil}
	case "gemini":
		message, messageOK := upstreamErrorText(envelope.Error["message"])
		state, _ := upstreamErrorText(envelope.Error["status"])
		if !messageOK || message == "" {
			return false
		}
		if state == "" {
			state = geminiErrorStatus(status)
		}
		body = map[string]any{"error": map[string]any{"code": status, "message": message, "status": state}}
	default:
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
	return true
}

func upstreamErrorText(value json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(value, &text) != nil || len(text) > 4096 {
		return "", false
	}
	return text, true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func hasCapability(values []string, scope string) bool {
	return providers.SupportsScope(values, scope)
}
func maximumOutput(body map[string]json.RawMessage) int64 {
	for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		var value int64
		if json.Unmarshal(body[name], &value) == nil && value > 0 {
			return value
		}
	}
	return 0
}
func jsonCardinality(raw json.RawMessage) int64 {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		return int64(len(items))
	}
	return 1
}
func geminiMaximumOutput(body map[string]any) int64 {
	config, _ := body["generationConfig"].(map[string]any)
	value, _ := config["maxOutputTokens"].(float64)
	return int64(value)
}
