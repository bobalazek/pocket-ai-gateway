package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// System One is the typed-decision wire format served by TypeSafe Jev and self-hosted Laya:
// unstructured state plus noul, choice, and score questions in, calibrated answers out.
const (
	maxSystemOneQuestions    = 64
	maxSystemOneChoices      = 255
	minSystemOneScoreLevels  = 2
	maxSystemOneScoreLevels  = 10
	maxSystemOneQuestionName = 128
)

// validateSystemOne checks the request shape before admission and returns the question count.
func validateSystemOne(envelope map[string]json.RawMessage) (int64, error) {
	if !jsonStructured(envelope["state"], true) {
		return 0, errors.New("state must be a non-empty string, object, or array")
	}
	if raw, exists := envelope["stream"]; exists && string(raw) != "false" {
		return 0, errors.New("stream is not supported for System One decisions")
	}
	var questions map[string]map[string]json.RawMessage
	if json.Unmarshal(envelope["questions"], &questions) != nil || len(questions) == 0 {
		return 0, errors.New("questions must be a non-empty object of question definitions")
	}
	if len(questions) > maxSystemOneQuestions {
		return 0, fmt.Errorf("at most %d questions are accepted", maxSystemOneQuestions)
	}
	for name, question := range questions {
		if name == "" || len(name) > maxSystemOneQuestionName {
			return 0, fmt.Errorf("question IDs must be 1-%d bytes", maxSystemOneQuestionName)
		}
		if question == nil {
			return 0, fmt.Errorf("question %q must be an object", name)
		}
		var kind string
		_ = json.Unmarshal(question["type"], &kind)
		if !jsonStructured(question["instructions"], true) {
			return 0, fmt.Errorf("question %q needs instructions as a string, object, or array", name)
		}
		criteria, hasCriteria := question["criteria"]
		switch kind {
		case "noul":
			if !hasCriteria || string(criteria) == "null" {
				continue
			}
			var sides map[string]json.RawMessage
			if json.Unmarshal(criteria, &sides) != nil {
				return 0, fmt.Errorf("question %q noul criteria must be an object with true and false", name)
			}
			for side, value := range sides {
				if side != "true" && side != "false" || !jsonStructured(value, false) {
					return 0, fmt.Errorf("question %q noul criteria accept only true and false descriptions", name)
				}
			}
		case "choice":
			var options map[string]json.RawMessage
			if json.Unmarshal(criteria, &options) != nil || len(options) == 0 || len(options) > maxSystemOneChoices {
				return 0, fmt.Errorf("question %q choice criteria must contain 1-%d options", name, maxSystemOneChoices)
			}
		case "score":
			var levels []json.RawMessage
			if json.Unmarshal(criteria, &levels) != nil || len(levels) < minSystemOneScoreLevels || len(levels) > maxSystemOneScoreLevels {
				return 0, fmt.Errorf("question %q score criteria must list %d-%d ordered levels", name, minSystemOneScoreLevels, maxSystemOneScoreLevels)
			}
			for _, level := range levels {
				if !jsonStructured(level, false) {
					return 0, fmt.Errorf("question %q score levels must be strings, objects, or arrays", name)
				}
			}
		default:
			return 0, fmt.Errorf("question %q type must be noul, choice, or score", name)
		}
	}
	return int64(len(questions)), nil
}

// jsonStructured reports whether a value is a string, object, or array; required values must be non-empty.
func jsonStructured(raw json.RawMessage, required bool) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return !required || typed != ""
	case map[string]any:
		return !required || len(typed) > 0
	case []any:
		return !required || len(typed) > 0
	}
	return false
}

func (handler *Handler) systemOneModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "systemone")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"name": m.ID, "description": m.Description, "release_date": nil})
	}
	writeJSON(w, map[string]any{"models": data})
}

// systemOneUpstreamError extracts a safe message from the error shapes System One servers use:
// Jev {"detail":{"error_type","message"}}, FastAPI {"detail":"..."} or {"detail":[{"msg"}]},
// Vercel {"error_type","message"}, and OpenJev's helper {"error":{"code","message"}}.
func systemOneUpstreamError(raw []byte, status int) (string, string, bool) {
	var envelope struct {
		Detail    json.RawMessage `json:"detail"`
		Error     json.RawMessage `json:"error"`
		ErrorType json.RawMessage `json:"error_type"`
		Message   json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", "", false
	}
	var detail struct {
		ErrorType json.RawMessage `json:"error_type"`
		Message   json.RawMessage `json:"message"`
		Msg       json.RawMessage `json:"msg"`
	}
	var list []json.RawMessage
	kindRaw, messageRaw := envelope.ErrorType, envelope.Message
	if message, ok := upstreamErrorText(envelope.Detail); ok && message != "" {
		messageRaw, _ = json.Marshal(message)
	} else if json.Unmarshal(envelope.Detail, &detail) == nil && len(envelope.Detail) > 0 {
		kindRaw, messageRaw = detail.ErrorType, detail.Message
	} else if json.Unmarshal(envelope.Detail, &list) == nil && len(list) > 0 && json.Unmarshal(list[0], &detail) == nil {
		messageRaw = detail.Msg
	} else if json.Unmarshal(envelope.Error, &detail) == nil && len(envelope.Error) > 0 {
		messageRaw = detail.Message
	}
	message, messageOK := upstreamErrorText(messageRaw)
	if !messageOK || message == "" {
		return "", "", false
	}
	kind, kindOK := upstreamErrorText(kindRaw)
	if !kindOK || kind == "" {
		kind = systemOneErrorType(status)
	}
	return kind, message, true
}

func systemOneErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return "validation_error"
	}
	return "api_error"
}
