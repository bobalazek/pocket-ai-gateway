package gateway

import (
	"bytes"
	"context"
	"database/sql"
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

const (
	conversationDeleteRetention    = 30 * 24 * time.Hour
	retainedConversations          = 10000
	retainedConversationBytes      = 1 << 30
	retainedOwnerConversations     = 2500
	retainedConversationOwnerBytes = 512 << 20
	retainedKeyConversations       = 250
	maxItemsPerConversation        = 10000
	maxConversationBytes           = 64 << 20
)

var (
	retainedConversationKeyBytes   int64 = 128 << 20
	errConversationLimit                 = errors.New("conversation retention limit reached")
	errConversationItemLimit             = errors.New("conversation item limit reached")
	errConversationRequest               = errors.New("invalid conversation request")
	errConversationContextTooLarge       = errors.New("conversation context exceeds request limit")
	errConversationChanged               = errors.New("conversation changed")
)

type conversation struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	CreatedAt int64          `json:"created_at"`
	Metadata  map[string]any `json:"metadata"`
}

type conversationAttachment struct {
	id, keyID, ownerID string
	revision           int64
	newItems           []json.RawMessage
	outputItems        []json.RawMessage
	requestBody        []byte
}

type conversationAttachmentContextKey struct{}

func responseConversationID(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return "", nil
	}
	var id string
	if json.Unmarshal(trimmed, &id) == nil && id != "" {
		return id, nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(trimmed, &value) != nil || len(value) != 1 || json.Unmarshal(value["id"], &id) != nil || id == "" {
		return "", errors.New("conversation must be a conversation ID or an object containing only id")
	}
	return id, nil
}

func (handler *Handler) prepareConversationResponse(ctx context.Context, principal keys.Principal, envelope map[string]json.RawMessage) (conversationAttachment, []byte, error) {
	id, err := responseConversationID(envelope["conversation"])
	if err != nil || id == "" {
		return conversationAttachment{}, nil, err
	}
	newItems, err := responseRequestConversationItems(envelope["input"])
	if err != nil {
		return conversationAttachment{}, nil, errConversationRequest
	}
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return conversationAttachment{}, nil, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", id, principal.KeyID).Scan(&revision); err != nil {
		return conversationAttachment{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body_json FROM conversation_items WHERE conversation_id=? ORDER BY ordinal", id)
	if err != nil {
		return conversationAttachment{}, nil, err
	}
	defer rows.Close()
	var history []json.RawMessage
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return conversationAttachment{}, nil, err
		}
		history = append(history, json.RawMessage(body))
	}
	if err := rows.Err(); err != nil {
		return conversationAttachment{}, nil, err
	}
	if err := rows.Close(); err != nil {
		return conversationAttachment{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return conversationAttachment{}, nil, err
	}
	storedItems := append(append([]json.RawMessage{}, history...), newItems...)
	storedInput, _ := json.Marshal(storedItems)
	envelope["input"] = storedInput
	delete(envelope, "conversation")
	storedBody, _ := json.Marshal(envelope)
	dispatch := make([]json.RawMessage, 0, len(storedItems))
	for _, item := range storedItems {
		var value map[string]json.RawMessage
		if json.Unmarshal(item, &value) != nil {
			return conversationAttachment{}, nil, errors.New("conversation contains an invalid item")
		}
		delete(value, "id")
		encoded, _ := json.Marshal(value)
		dispatch = append(dispatch, encoded)
	}
	encodedItems, _ := json.Marshal(dispatch)
	envelope["input"] = encodedItems
	body, err := json.Marshal(envelope)
	if err == nil && len(body) > maxInferenceBody {
		err = errConversationContextTooLarge
	}
	return conversationAttachment{id: id, keyID: principal.KeyID, ownerID: principal.OwnerUserID, revision: revision, newItems: newItems, requestBody: storedBody}, body, err
}

func responseRequestConversationItems(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []json.RawMessage{}, nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
		return normalizeConversationItemsLimit([]json.RawMessage{item}, true, maxItemsPerConversation)
	}
	var items []json.RawMessage
	if json.Unmarshal(trimmed, &items) != nil {
		return nil, errors.New("input must be text or an input-item array")
	}
	return normalizeConversationItemsLimit(items, true, maxItemsPerConversation)
}

func responseWithConversation(body []byte, attachment *conversationAttachment) ([]byte, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return nil, errors.New("provider response is not a JSON object")
	}
	var object, status string
	if json.Unmarshal(value["object"], &object) != nil || object != "response" || json.Unmarshal(value["status"], &status) != nil || status == "" {
		return nil, errors.New("provider response is not a valid Response object")
	}
	var output []json.RawMessage
	if json.Unmarshal(value["output"], &output) != nil {
		return nil, errors.New("provider response output is invalid")
	}
	normalized, err := normalizeConversationItemsLimit(output, true, maxItemsPerConversation)
	if err != nil {
		return nil, err
	}
	attachment.outputItems = normalized
	value["output"], _ = json.Marshal(normalized)
	value["conversation"], _ = json.Marshal(map[string]string{"id": attachment.id})
	return json.Marshal(value)
}

func appendConversationTurn(ctx context.Context, tx *sql.Tx, attachment conversationAttachment, now int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE conversations SET revision=revision+1 WHERE id=? AND key_id=? AND deleted_at IS NULL AND revision=?", attachment.id, attachment.keyID, attachment.revision)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errConversationChanged
	}
	items := append(append([]json.RawMessage{}, attachment.newItems...), attachment.outputItems...)
	if err := checkConversationCapacity(ctx, tx, attachment.ownerID, attachment.keyID, 0, conversationItemsSize(items)); err != nil {
		return err
	}
	return insertConversationItems(ctx, tx, attachment.id, items, now)
}

func (handler *Handler) storeConversationTurn(ctx context.Context, attachment conversationAttachment) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendConversationTurn(ctx, tx, attachment, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (handler *Handler) createConversation(response http.ResponseWriter, request *http.Request) {
	principal, body, ok := handler.conversationRequest(response, request)
	if !ok {
		return
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		body = []byte("{}")
	}
	var input struct {
		Items    []json.RawMessage `json:"items"`
		Metadata json.RawMessage   `json:"metadata"`
	}
	if json.Unmarshal(body, &input) != nil || !onlyJSONFields(body, "items", "metadata") {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	metadata, err := conversationMetadata(input.Metadata)
	if err != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	metadataJSON, _ := json.Marshal(metadata)
	items, err := normalizeConversationItems(input.Items, true)
	if err != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	idPart, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation could not be created")
		return
	}
	id, now := "conv_"+idPart, time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = purgeDeletedConversations(request.Context(), tx, now-conversationDeleteRetention.Milliseconds())
	}
	if err == nil {
		err = checkConversationCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(metadataJSON))+conversationItemsSize(items))
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "INSERT INTO conversations(id,owner_user_id,key_id,metadata_json,created_at) VALUES(?,?,?,?,?)", id, principal.OwnerUserID, principal.KeyID, metadataJSON, now)
	}
	if err == nil {
		err = insertConversationItems(request.Context(), tx, id, items, now)
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "UPDATE conversations SET revision=revision+1 WHERE id=?", id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		status, code, message := http.StatusServiceUnavailable, "gateway_unavailable", "Conversation could not be created"
		if errors.Is(err, errConversationLimit) {
			status, code, message = http.StatusTooManyRequests, "rate_limit_exceeded", err.Error()
		}
		handler.writeError(response, "responses", status, code, message)
		return
	}
	writeJSON(response, conversation{ID: id, Object: "conversation", CreatedAt: now / 1000, Metadata: metadata})
}

func (handler *Handler) getConversation(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return
	}
	value, err := readConversation(request.Context(), handler.database, request.PathValue("conversation_id"), principal.KeyID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation is unavailable")
		return
	}
	writeJSON(response, value)
}

func (handler *Handler) updateConversation(response http.ResponseWriter, request *http.Request) {
	principal, body, ok := handler.conversationRequest(response, request)
	if !ok {
		return
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(body, &input) != nil || input == nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	raw, exists := input["metadata"]
	if !exists || len(input) != 1 {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "metadata is required")
		return
	}
	metadata, err := conversationMetadata(raw)
	if err != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	metadataJSON, _ := json.Marshal(metadata)
	id := request.PathValue("conversation_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var oldSize int64
		err = tx.QueryRowContext(request.Context(), "SELECT length(metadata_json) FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", id, principal.KeyID).Scan(&oldSize)
		growth := int64(len(metadataJSON)) - oldSize
		if err == nil && growth > 0 {
			err = checkConversationCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 0, growth)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "UPDATE conversations SET metadata_json=? WHERE id=? AND key_id=? AND deleted_at IS NULL", metadataJSON, id, principal.KeyID)
	}
	var value conversation
	if err == nil {
		value, err = readConversation(request.Context(), tx, id, principal.KeyID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation not found")
		return
	}
	if err != nil {
		status, code, message := http.StatusServiceUnavailable, "gateway_unavailable", "Conversation could not be updated"
		if errors.Is(err, errConversationLimit) {
			status, code, message = http.StatusTooManyRequests, "rate_limit_exceeded", err.Error()
		}
		handler.writeError(response, "responses", status, code, message)
		return
	}
	writeJSON(response, value)
}

func (handler *Handler) deleteConversation(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("conversation_id")
	result, err := handler.database.ExecContext(request.Context(), "UPDATE conversations SET deleted_at=? WHERE id=? AND key_id=? AND deleted_at IS NULL", time.Now().UnixMilli(), id, principal.KeyID)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation could not be deleted")
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "object": "conversation.deleted", "deleted": true})
}

func (handler *Handler) createConversationItems(response http.ResponseWriter, request *http.Request) {
	principal, body, ok := handler.conversationRequest(response, request)
	if !ok {
		return
	}
	if hasInclude(request) {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "include projections are not supported for conversation items")
		return
	}
	var input struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(body, &input) != nil || !onlyJSONFields(body, "items") {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	items, err := normalizeConversationItems(input.Items, false)
	if err != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id, now := request.PathValue("conversation_id"), time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var exists bool
		err = tx.QueryRowContext(request.Context(), "SELECT EXISTS(SELECT 1 FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL)", id, principal.KeyID).Scan(&exists)
		if err == nil && !exists {
			err = sql.ErrNoRows
		}
	}
	if err == nil {
		err = checkConversationCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 0, conversationItemsSize(items))
	}
	if err == nil {
		err = insertConversationItems(request.Context(), tx, id, items, now)
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "UPDATE conversations SET revision=revision+1 WHERE id=?", id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation not found")
		return
	}
	if err != nil {
		status, code, message := http.StatusServiceUnavailable, "gateway_unavailable", "Conversation items could not be added"
		if errors.Is(err, errConversationItemLimit) || errors.Is(err, errConversationLimit) {
			status, code, message = http.StatusTooManyRequests, "rate_limit_exceeded", err.Error()
		}
		handler.writeError(response, "responses", status, code, message)
		return
	}
	writeJSON(response, conversationItemPage(items, false))
}

func (handler *Handler) listConversationItems(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return
	}
	if hasInclude(request) {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "include projections are not supported for conversation items")
		return
	}
	order := request.URL.Query().Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "order must be asc or desc")
		return
	}
	limit := 20
	if raw := request.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
	}
	items, more, err := readConversationItems(request.Context(), handler.database, request.PathValue("conversation_id"), principal.KeyID, request.URL.Query().Get("after"), order, limit)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation or cursor not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation items are unavailable")
		return
	}
	writeJSON(response, conversationItemPage(items, more))
}

func (handler *Handler) getConversationItem(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return
	}
	if hasInclude(request) {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "include projections are not supported for conversation items")
		return
	}
	var body []byte
	err := handler.database.QueryRowContext(request.Context(), `SELECT conversation_items.body_json FROM conversation_items JOIN conversations ON conversations.id=conversation_items.conversation_id WHERE conversations.id=? AND conversations.key_id=? AND conversations.deleted_at IS NULL AND conversation_items.id=?`, request.PathValue("conversation_id"), principal.KeyID, request.PathValue("item_id")).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation item not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation item is unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) deleteConversationItem(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return
	}
	id, itemID := request.PathValue("conversation_id"), request.PathValue("item_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
	}
	var result sql.Result
	if err == nil {
		result, err = tx.ExecContext(request.Context(), `DELETE FROM conversation_items WHERE conversation_id=? AND id=? AND EXISTS(SELECT 1 FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL)`, id, itemID, id, principal.KeyID)
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation item could not be deleted")
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Conversation item not found")
		return
	}
	if _, err = tx.ExecContext(request.Context(), "UPDATE conversations SET revision=revision+1 WHERE id=?", id); err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation item could not be deleted")
		return
	}
	value, err := readConversation(request.Context(), tx, id, principal.KeyID)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation is unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Conversation item could not be deleted")
		return
	}
	writeJSON(response, value)
}

func (handler *Handler) conversationPrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "responses")
	if ok && !principalHasScope(principal.Scopes, "responses:generate") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) conversationRequest(response http.ResponseWriter, request *http.Request) (keys.Principal, []byte, bool) {
	principal, ok := handler.conversationPrincipal(response, request)
	if !ok {
		return keys.Principal{}, nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "responses", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return keys.Principal{}, nil, false
	}
	return principal, body, true
}

func conversationMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil || len(value) > 16 {
		return nil, errors.New("metadata must contain at most 16 string pairs")
	}
	for key, item := range value {
		text, ok := item.(string)
		if !ok || utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(text) > 512 {
			return nil, errors.New("metadata keys must be at most 64 characters and values must be strings of at most 512 characters")
		}
	}
	return value, nil
}

func normalizeConversationItems(raw []json.RawMessage, allowEmpty bool) ([]json.RawMessage, error) {
	return normalizeConversationItemsLimit(raw, allowEmpty, 20)
}

func normalizeConversationItemsLimit(raw []json.RawMessage, allowEmpty bool, limit int) ([]json.RawMessage, error) {
	if len(raw) == 0 && allowEmpty {
		return []json.RawMessage{}, nil
	}
	if len(raw) < 1 {
		return nil, errors.New("items must contain at least one object")
	}
	if len(raw) > limit {
		return nil, errors.New("too many conversation items")
	}
	items := make([]json.RawMessage, 0, len(raw))
	for _, source := range raw {
		var item map[string]any
		if json.Unmarshal(source, &item) != nil || item == nil {
			return nil, errors.New("each item must be an object with a type")
		}
		itemType, _ := item["type"].(string)
		if itemType == "" {
			if _, roleOK := item["role"].(string); !roleOK {
				return nil, errors.New("each item must be a supported input item")
			}
			itemType, item["type"] = "message", "message"
		}
		if err := rejectCompactReferences(item); err != nil {
			return nil, err
		}
		if err := validateConversationItem(itemType, item); err != nil {
			return nil, err
		}
		id, err := credentials.RandomToken(18)
		if err != nil {
			return nil, err
		}
		item["id"] = "citem_" + id
		if item["status"] == nil {
			item["status"] = "completed"
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		items = append(items, encoded)
	}
	return items, nil
}

func validateConversationItem(itemType string, item map[string]any) error {
	if status, exists := item["status"]; exists && status != nil {
		value, ok := status.(string)
		if !ok || (value != "in_progress" && value != "completed" && value != "incomplete") {
			return errors.New("item status must be in_progress, completed, or incomplete")
		}
	}
	switch itemType {
	case "message":
		role, roleOK := item["role"].(string)
		if !roleOK || (role != "user" && role != "assistant" && role != "system" && role != "developer") {
			return errors.New("message role must be user, assistant, system, or developer")
		}
		switch content := item["content"].(type) {
		case string:
			item["content"] = []any{map[string]any{"type": "input_text", "text": content}}
		case []any:
			if len(content) == 0 {
				return errors.New("message content must not be empty")
			}
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok || !validConversationContent(part) {
					return errors.New("message content contains an unsupported or malformed part")
				}
			}
		default:
			return errors.New("message content must be text or a non-empty content array")
		}
	case "function_call":
		if !hasStrings(item, "arguments", "call_id", "name") {
			return errors.New("function_call requires arguments, call_id, and name")
		}
	case "function_call_output":
		if _, ok := item["output"].(string); !ok || !hasStrings(item, "call_id") {
			return errors.New("function_call_output requires call_id and string output")
		}
	default:
		return errors.New("unsupported conversation item type: " + itemType)
	}
	return nil
}

func validConversationContent(part map[string]any) bool {
	switch part["type"] {
	case "input_text", "output_text":
		_, ok := part["text"].(string)
		if !ok || part["type"] != "output_text" {
			return ok
		}
		if part["annotations"] == nil {
			part["annotations"] = []any{}
			return true
		}
		_, ok = part["annotations"].([]any)
		return ok
	case "refusal":
		_, ok := part["refusal"].(string)
		return ok
	case "input_image":
		url, urlOK := part["image_url"].(string)
		detail, detailOK := part["detail"].(string)
		return urlOK && url != "" && detailOK && (detail == "auto" || detail == "low" || detail == "high" || detail == "original")
	default:
		return false
	}
}

func hasStrings(item map[string]any, fields ...string) bool {
	for _, field := range fields {
		if value, ok := item[field].(string); !ok || value == "" {
			return false
		}
	}
	return true
}

func insertConversationItems(ctx context.Context, tx *sql.Tx, conversationID string, items []json.RawMessage, now int64) error {
	var count, size, ordinal int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(body_json)),0),COALESCE(MAX(ordinal),0) FROM conversation_items WHERE conversation_id=?", conversationID).Scan(&count, &size, &ordinal); err != nil {
		return err
	}
	incoming := int64(0)
	for _, item := range items {
		incoming += int64(len(item))
	}
	if count+int64(len(items)) > maxItemsPerConversation || size+incoming > maxConversationBytes {
		return errConversationItemLimit
	}
	for _, item := range items {
		ordinal++
		var value map[string]json.RawMessage
		_ = json.Unmarshal(item, &value)
		var id string
		_ = json.Unmarshal(value["id"], &id)
		if _, err := tx.ExecContext(ctx, "INSERT INTO conversation_items(conversation_id,id,ordinal,body_json,created_at) VALUES(?,?,?,?,?)", conversationID, id, ordinal, item, now); err != nil {
			return err
		}
	}
	return nil
}

func conversationItemsSize(items []json.RawMessage) int64 {
	var size int64
	for _, item := range items {
		size += int64(len(item))
	}
	return size
}

func purgeDeletedConversations(ctx context.Context, tx *sql.Tx, before int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_items WHERE conversation_id IN (SELECT id FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at<?)", before); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at<?", before)
	return err
}

func checkConversationCapacity(ctx context.Context, query responseQueryer, ownerID, keyID string, incomingCount, incomingBytes int64) error {
	var count, size, ownerCount, ownerSize, keyCount, keySize int64
	err := query.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0)),0),
		COALESCE(SUM(owner_user_id=?),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0) ELSE 0 END),0),
		COALESCE(SUM(key_id=?),0),COALESCE(SUM(CASE WHEN key_id=? THEN length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0) ELSE 0 END),0)
		FROM conversations`, ownerID, ownerID, keyID, keyID).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
	if err != nil {
		return err
	}
	if count+incomingCount > retainedConversations || size+incomingBytes > retainedConversationBytes || ownerCount+incomingCount > retainedOwnerConversations || ownerSize+incomingBytes > retainedConversationOwnerBytes || keyCount+incomingCount > retainedKeyConversations || keySize+incomingBytes > retainedConversationKeyBytes {
		return errConversationLimit
	}
	return nil
}

func readConversation(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id, keyID string) (conversation, error) {
	var value conversation
	var metadata []byte
	var created int64
	err := query.QueryRowContext(ctx, "SELECT id,metadata_json,created_at FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", id, keyID).Scan(&value.ID, &metadata, &created)
	if err != nil {
		return value, err
	}
	value.Object, value.CreatedAt = "conversation", created/1000
	if json.Unmarshal(metadata, &value.Metadata) != nil {
		return conversation{}, errors.New("invalid stored conversation metadata")
	}
	return value, nil
}

func readConversationItems(ctx context.Context, database *sql.DB, id, keyID, after, order string, limit int) ([]json.RawMessage, bool, error) {
	if _, err := readConversation(ctx, database, id, keyID); err != nil {
		return nil, false, err
	}
	var cursor int64
	if after != "" {
		if err := database.QueryRowContext(ctx, "SELECT ordinal FROM conversation_items WHERE conversation_id=? AND id=?", id, after).Scan(&cursor); err != nil {
			return nil, false, err
		}
	}
	comparison, direction := ">", "ASC"
	if order == "desc" {
		comparison, direction = "<", "DESC"
	}
	query := "SELECT body_json FROM conversation_items WHERE conversation_id=? AND (?='' OR ordinal " + comparison + " ?) ORDER BY ordinal " + direction + " LIMIT ?"
	rows, err := database.QueryContext(ctx, query, id, after, cursor, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, limit)
	more := false
	for rows.Next() {
		var item []byte
		if err := rows.Scan(&item); err != nil {
			return nil, false, err
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, json.RawMessage(item))
	}
	return items, more, rows.Err()
}

func conversationItemPage(items []json.RawMessage, more bool) map[string]any {
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": "", "last_id": ""}
	if len(items) > 0 {
		var first, last map[string]json.RawMessage
		_ = json.Unmarshal(items[0], &first)
		_ = json.Unmarshal(items[len(items)-1], &last)
		var firstID, lastID string
		_ = json.Unmarshal(first["id"], &firstID)
		_ = json.Unmarshal(last["id"], &lastID)
		result["first_id"], result["last_id"] = firstID, lastID
	}
	return result
}

func hasInclude(request *http.Request) bool {
	return len(request.URL.Query()["include"]) > 0 || len(request.URL.Query()["include[]"]) > 0
}

func onlyJSONFields(body []byte, allowed ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return false
	}
	for field := range fields {
		found := false
		for _, name := range allowed {
			found = found || field == name
		}
		if !found {
			return false
		}
	}
	return true
}
