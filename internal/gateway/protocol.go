package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
		googleStatus := code
		if !strings.Contains(code, "_") {
			googleStatus = strings.ToUpper(code)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": message, "status": googleStatus}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}})
	}
}
func writeNativeUpstreamError(w http.ResponseWriter, dialect string, status int, raw []byte) bool {
	openAI := dialect == "openai" || dialect == "responses" || dialect == "responses_compact"
	clientStatus := status == http.StatusBadRequest || status == http.StatusConflict || status == http.StatusUnprocessableEntity || status == http.StatusTooManyRequests
	if !openAI || !clientStatus {
		return false
	}
	var envelope struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
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
		var text string
		if json.Unmarshal(value, &text) != nil || len(text) > 4096 {
			return false
		}
		safe[name] = text
	}
	if message, _ := safe["message"].(string); message == "" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": safe})
	return true
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func nativeAdapter(dialect, adapter string) bool {
	return dialect == adapter || (dialect == "openai" && adapter == "openai_compatible")
}
func hasCapability(values []string, scope string) bool {
	wanted := map[string]string{"chat:generate": "chat", "responses:generate": "chat", "embeddings:generate": "embeddings", "tokens:count": "count_tokens", "moderations:classify": "moderations", "images:generate": "images", "images:edit": "image_edit", "images:variation": "image_variation", "audio:speech": "audio_speech", "audio:transcribe": "audio_transcription", "audio:translate": "audio_translation"}[scope]
	for _, v := range values {
		if v == wanted || v == strings.ReplaceAll(scope, ":", "_") {
			return true
		}
	}
	return false
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
