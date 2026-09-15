package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

type storedChatContextKey struct{}

type storedChatRequest struct {
	request  []byte
	metadata json.RawMessage
}

type preparedChatCompletion struct {
	id       string
	body     []byte
	created  int64
	storedAt time.Time
}

func (handler *Handler) chatCompletions(response http.ResponseWriter, request *http.Request) {
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
	stored, err := jsonBoolean(envelope, "store", false)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	stream, err := jsonBoolean(envelope, "stream", false)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if stored && stream {
		handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", "stored streaming Chat Completions are not supported")
		return
	}
	if stored {
		metadata, err := validateChatMetadata(envelope["metadata"], false)
		if err != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		messages, err := validateStoredChatMessages(envelope["messages"])
		if err != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		storedEnvelope := make(map[string]json.RawMessage, len(envelope))
		for name, value := range envelope {
			storedEnvelope[name] = value
		}
		storedEnvelope["messages"] = messages
		storedBody, err := json.Marshal(storedEnvelope)
		if err != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body could not be normalized")
			return
		}
		storage := &storedChatRequest{request: storedBody, metadata: metadata}
		request = request.WithContext(context.WithValue(request.Context(), storedChatContextKey{}, storage))
	}
	handler.forwardAuthorized(response, request, "openai", "chat:generate", "chat/completions", "", nil, principal, body)
}

func jsonBoolean(envelope map[string]json.RawMessage, name string, fallback bool) (bool, error) {
	raw, exists := envelope[name]
	if !exists {
		return fallback, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, errors.New(name + " must be a boolean")
	}
	return value, nil
}

func validateChatMetadata(raw json.RawMessage, required bool) (json.RawMessage, error) {
	if len(raw) == 0 {
		if required {
			return nil, errors.New("metadata is required")
		}
		return json.RawMessage(`{}`), nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage(`null`), nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil || len(value) > 16 {
		return nil, errors.New("metadata must contain at most 16 string pairs")
	}
	for key, encoded := range value {
		var text string
		if json.Unmarshal(encoded, &text) != nil || utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(text) > 512 {
			return nil, errors.New("metadata keys must be at most 64 characters and values must be strings of at most 512 characters")
		}
	}
	return append(json.RawMessage(nil), raw...), nil
}

func validateStoredChatMessages(raw json.RawMessage) (json.RawMessage, error) {
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return nil, errors.New("messages must contain at least one object")
	}
	for index, rawMessage := range messages {
		var message map[string]json.RawMessage
		var role string
		if json.Unmarshal(rawMessage, &message) != nil || message == nil || json.Unmarshal(message["role"], &role) != nil || role == "" {
			return nil, errors.New("each message must be an object with a role")
		}
		content, exists := message["content"]
		if !exists {
			message["content"] = json.RawMessage(`null`)
			messages[index], _ = json.Marshal(message)
			continue
		}
		trimmed := bytes.TrimSpace(content)
		if bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		var text string
		if json.Unmarshal(content, &text) == nil {
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(content, &parts) != nil || len(parts) == 0 {
			return nil, errors.New("stored message content must be a string, null, or a non-empty text/image_url part array")
		}
		for _, part := range parts {
			var kind string
			if part == nil || json.Unmarshal(part["type"], &kind) != nil {
				return nil, errors.New("stored message content parts require a type")
			}
			switch kind {
			case "text":
				var value string
				if _, exists := part["text"]; !exists || json.Unmarshal(part["text"], &value) != nil {
					return nil, errors.New("text content parts require a string text value")
				}
			case "image_url":
				var image map[string]json.RawMessage
				var url string
				if json.Unmarshal(part["image_url"], &image) != nil || image == nil || json.Unmarshal(image["url"], &url) != nil || url == "" {
					return nil, errors.New("image_url content parts require a non-empty image_url.url string")
				}
			default:
				return nil, errors.New("stored message content parts support only text and image_url")
			}
		}
	}
	return json.Marshal(messages)
}

func prepareStoredChatCompletion(modelID string, body, metadata json.RawMessage) (preparedChatCompletion, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return preparedChatCompletion{}, errors.New("provider response is not a JSON object")
	}
	var object string
	var choices []json.RawMessage
	if json.Unmarshal(value["object"], &object) != nil || object != "chat.completion" || json.Unmarshal(value["choices"], &choices) != nil {
		return preparedChatCompletion{}, errors.New("provider response is not a valid Chat Completion object")
	}
	for _, rawChoice := range choices {
		var choice map[string]json.RawMessage
		var message map[string]json.RawMessage
		if json.Unmarshal(rawChoice, &choice) != nil || choice == nil || json.Unmarshal(choice["message"], &message) != nil || message == nil {
			return preparedChatCompletion{}, errors.New("provider response is not a valid Chat Completion object")
		}
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		return preparedChatCompletion{}, err
	}
	now := time.Now()
	id := "chatcmpl_" + token
	value["id"], _ = json.Marshal(id)
	value["model"], _ = json.Marshal(modelID)
	value["object"] = json.RawMessage(`"chat.completion"`)
	value["metadata"] = append(json.RawMessage(nil), metadata...)
	var created int64
	if json.Unmarshal(value["created"], &created) != nil || created < 1 || created > 1<<53-1 {
		created = now.Unix()
		value["created"] = json.RawMessage(strconv.FormatInt(created, 10))
	}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) > storedChatListBytes {
		return preparedChatCompletion{}, errors.New("stored Chat Completion exceeds 16 MiB response limit")
	}
	return preparedChatCompletion{id: id, body: encoded, created: created, storedAt: now}, err
}

func (handler *Handler) storeChatCompletion(ctx context.Context, requestID string, principal keys.Principal, modelID string, request storedChatRequest, value preparedChatCompletion) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkRetainedResponseCapacity(ctx, tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(request.request)+len(value.body)+len(request.metadata))); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stored_chat_completions(id,owner_user_id,key_id,model_id,body_json,request_json,metadata_json,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, value.id, principal.OwnerUserID, principal.KeyID, modelID, value.body, request.request, request.metadata, value.created*1000, value.storedAt.Add(storedResponseLifetime).UnixMilli())
	if err != nil {
		return err
	}
	if err := replaceStoredChatMetadata(ctx, tx, value.id, request.metadata); err != nil {
		return err
	}
	if err := handler.usage.FinalizeRequestTx(ctx, tx, requestID, "succeeded"); err != nil {
		return err
	}
	return tx.Commit()
}
