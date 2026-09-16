package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func (handler *Handler) responses(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "responses")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "responses", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	if containsLocalFileReference(envelope["input"]) {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "Gateway file references are not supported by Responses")
		return
	}
	var background bool
	if raw, exists := envelope["background"]; exists && json.Unmarshal(raw, &background) != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "background must be a boolean")
		return
	}
	webSearch, webSearchErr := validateResponseWebSearch(envelope)
	if webSearchErr != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", webSearchErr.Error())
		return
	}
	if webSearch.enabled && !principalHasScope(principal.Scopes, "responses:web_search") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Web search access is not permitted")
		return
	}
	fileSearch, fileSearchErr := validateResponseFileSearch(envelope)
	if fileSearchErr != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", fileSearchErr.Error())
		return
	}
	if fileSearch.enabled && !principalHasScope(principal.Scopes, responseFileSearchScope) {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "File search access is not permitted")
		return
	}
	if fileSearch.enabled {
		if err := handler.validateResponseFileSearchStores(request.Context(), principal.KeyID, fileSearch); errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Vector Store not found")
			return
		} else if err != nil {
			handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store search is unavailable")
			return
		}
	}
	conversationID, conversationErr := responseConversationID(envelope["conversation"])
	if conversationErr != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", conversationErr.Error())
		return
	}
	var attachment *conversationAttachment
	if conversationID != "" {
		if !principalHasScope(principal.Scopes, "responses:generate") {
			handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
			return
		}
		var stream bool
		if raw, exists := envelope["stream"]; exists && json.Unmarshal(raw, &stream) != nil {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "stream must be a boolean")
			return
		}
		prepared, expanded, err := handler.prepareConversationResponse(request.Context(), principal, envelope)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation not found")
			return
		}
		if errors.Is(err, errConversationRequest) {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Conversation input is invalid")
			return
		}
		if errors.Is(err, errConversationContextTooLarge) {
			handler.writeError(response, "responses", http.StatusRequestEntityTooLarge, "request_too_large", "Conversation context exceeds 16 MiB")
			return
		}
		if err != nil {
			handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation is unavailable")
			return
		}
		attachment = &prepared
		body = expanded
	}
	if !background {
		if attachment != nil {
			request = request.WithContext(context.WithValue(request.Context(), conversationAttachmentContextKey{}, *attachment))
		}
		handler.forwardAuthorized(response, request, "responses", "responses:generate", "responses", "", nil, principal, body)
		return
	}
	handler.enqueueResponse(response, request, principal, envelope, body, attachment)
}

func (handler *Handler) compactResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "responses_compact")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "responses_compact", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var model string
	_ = json.Unmarshal(envelope["model"], &model)
	if model == "" || len(envelope["input"]) == 0 {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "invalid_request", "model and input are required")
		return
	}
	if err := validateCompactInput(envelope["input"]); err != nil {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	if _, exists := envelope["stream"]; exists {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", "stream is not supported by compact")
		return
	}
	for _, field := range []string{"conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) {
			handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", field+" is not supported by compact")
			return
		}
	}
	handler.forwardAuthorized(response, request, "responses_compact", "responses:generate", "responses/compact", "", nil, principal, body)
}

func (handler *Handler) responseInputTokens(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "openai")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var model string
	_ = json.Unmarshal(envelope["model"], &model)
	if model == "" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if raw := bytes.TrimSpace(envelope["input"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := validateCompactInput(raw); err != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	for _, field := range []string{"stream", "store", "background"} {
		if _, exists := envelope[field]; exists {
			handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", field+" is not supported by input token counting")
			return
		}
	}
	for _, field := range []string{"conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) {
			handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", field+" is not supported by input token counting")
			return
		}
	}
	if raw := envelope["tools"]; len(raw) > 0 {
		var tools []map[string]any
		if json.Unmarshal(raw, &tools) != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "tools must be an array")
			return
		}
		for _, tool := range tools {
			if kind, _ := tool["type"].(string); kind != "function" {
				handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", "only function tools are supported by input token counting")
				return
			}
		}
	}
	handler.forwardAuthorized(response, request, "openai", "tokens:count", "responses/input_tokens", "", nil, principal, body)
}

func validateCompactInput(raw json.RawMessage) error {
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return errors.New("input must be a string or input-item array")
	}
	if _, ok := input.(string); ok {
		return nil
	}
	items, ok := input.([]any)
	if !ok {
		return errors.New("input must be a string or input-item array")
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return errors.New("input array must contain only input-item objects")
		}
	}
	if walkLocalFileReferences(items) {
		return errors.New("gateway file references are not supported")
	}
	return rejectCompactReferences(items)
}

func rejectCompactReferences(value any) error {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if err := rejectCompactReferences(item); err != nil {
				return err
			}
		}
	case map[string]any:
		kind, _ := value["type"].(string)
		_, hasType := value["type"]
		id, _ := value["id"].(string)
		if kind == "item_reference" || (id != "" && (!hasType || value["type"] == nil)) {
			return errors.New("provider item references are not supported")
		}
		fileReference := (kind == "input_file" || kind == "input_image" || kind == "computer_screenshot") && value["file_id"] != nil && value["file_id"] != ""
		containerReference := kind == "container_reference" && value["container_id"] != nil && value["container_id"] != ""
		if fileReference || containerReference {
			return errors.New("provider resource references are not supported")
		}
		for _, item := range value {
			if err := rejectCompactReferences(item); err != nil {
				return err
			}
		}
	}
	return nil
}
