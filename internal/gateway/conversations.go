package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	conversationDeleteRetention = 30 * 24 * time.Hour
)

type conversation struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	CreatedAt int64          `json:"created_at"`
	Metadata  map[string]any `json:"metadata"`
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
