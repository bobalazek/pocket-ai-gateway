package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func (handler *Handler) getChatCompletion(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.chatPrincipal(response, request)
	if !ok {
		return
	}
	body, err := handler.readChatCompletion(request.Context(), request.PathValue("completion_id"), principal.KeyID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Chat Completion not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completion is unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) readChatCompletion(ctx context.Context, id, keyID string) ([]byte, error) {
	var body []byte
	err := handler.database.QueryRowContext(ctx, `SELECT body_json FROM stored_chat_completions WHERE id=? AND key_id=? AND expires_at>?`, id, keyID, time.Now().UnixMilli()).Scan(&body)
	return body, err
}

func (handler *Handler) updateChatCompletion(response http.ResponseWriter, request *http.Request) {
	principal, body, ok := handler.chatRequest(response, request)
	if !ok {
		return
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(body, &input) != nil || input == nil || len(input) != 1 {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must contain only metadata")
		return
	}
	metadata, err := validateChatMetadata(input["metadata"], true)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := request.PathValue("completion_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var stored []byte
		var oldBodyBytes, oldMetadataBytes int64
		err = tx.QueryRowContext(request.Context(), `SELECT body_json,length(body_json),length(metadata_json) FROM stored_chat_completions WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli()).Scan(&stored, &oldBodyBytes, &oldMetadataBytes)
		if err == nil {
			var value map[string]json.RawMessage
			if json.Unmarshal(stored, &value) != nil || value == nil {
				err = errors.New("invalid stored completion")
			} else {
				value["metadata"] = metadata
				stored, err = json.Marshal(value)
			}
		}
		growth := storedChatMetadataGrowth(oldBodyBytes, oldMetadataBytes, stored, metadata)
		if err == nil && growth > 0 {
			err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 0, growth)
		}
		if err == nil {
			var result sql.Result
			result, err = tx.ExecContext(request.Context(), `UPDATE stored_chat_completions SET body_json=?,metadata_json=? WHERE id=? AND key_id=? AND expires_at>?`, stored, metadata, id, principal.KeyID, time.Now().UnixMilli())
			if err == nil {
				if changed, _ := result.RowsAffected(); changed != 1 {
					err = sql.ErrNoRows
				}
			}
		}
		if err == nil {
			err = replaceStoredChatMetadata(request.Context(), tx, id, metadata)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err == nil {
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			_, _ = response.Write(stored)
			return
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Chat Completion not found")
		return
	}
	if errors.Is(err, errRetainedResourceLimit) {
		handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference result retention limit reached")
		return
	}
	handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completion could not be updated")
}

func storedChatMetadataGrowth(oldBodyBytes, oldMetadataBytes int64, body, metadata []byte) int64 {
	return int64(len(body)+len(metadata)) - oldBodyBytes - oldMetadataBytes
}

func (handler *Handler) deleteChatCompletion(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.chatPrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("completion_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM stored_chat_completions WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completion could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Chat Completion not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "object": "chat.completion.deleted", "deleted": true})
}

type storedChatRow struct {
	id   string
	body json.RawMessage
}

const storedChatListBytes = maxInferenceBody - 1024

func (handler *Handler) listChatCompletions(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.chatPrincipal(response, request)
	if !ok {
		return
	}
	limit, order, err := pageOptions(request, "asc")
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	filters, err := chatMetadataFilters(request)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	model, after, err := singleChatQueryValues(request)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	predicate, arguments := chatCompletionPredicate(principal.KeyID, time.Now().UnixMilli(), model, filters)
	comparison := ">"
	if order == "DESC" {
		comparison = "<"
	}
	if after != "" {
		var createdAt int64
		cursorArguments := append(append([]any(nil), arguments...), after)
		err = handler.database.QueryRowContext(request.Context(), `SELECT c.created_at FROM stored_chat_completions c WHERE `+predicate+` AND c.id=?`, cursorArguments...).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after is not a valid Chat Completion cursor")
			return
		}
		if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completions are unavailable")
			return
		}
		predicate += ` AND (c.created_at ` + comparison + ` ? OR (c.created_at=? AND c.id ` + comparison + ` ?))`
		arguments = append(arguments, createdAt, createdAt, after)
	}
	arguments = append(arguments, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), `SELECT c.id,c.body_json FROM stored_chat_completions c WHERE `+predicate+` ORDER BY c.created_at `+order+`,c.id `+order+` LIMIT ?`, arguments...)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completions are unavailable")
		return
	}
	defer rows.Close()
	data := make([]json.RawMessage, 0, limit)
	totalBytes, more := 0, false
	for rows.Next() {
		var item storedChatRow
		if err = rows.Scan(&item.id, &item.body); err != nil {
			break
		}
		if len(data) == limit || len(data) > 0 && totalBytes+len(item.body) > storedChatListBytes {
			more = true
			break
		}
		if len(item.body) > storedChatListBytes {
			err = errors.New("stored Chat Completion exceeds list response limit")
			break
		}
		data = append(data, item.body)
		totalBytes += len(item.body)
	}
	if err == nil && !more {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completions are unavailable")
		return
	}
	writeCursorPage(response, data, more)
}

func chatMetadataFilters(request *http.Request) (map[string]string, error) {
	filters := map[string]string{}
	for name, values := range request.URL.Query() {
		if !strings.HasPrefix(name, "metadata[") || !strings.HasSuffix(name, "]") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "metadata["), "]")
		if key == "" || len(values) != 1 || utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(values[0]) > 512 || len(filters) >= 16 {
			return nil, errors.New("metadata filters must contain at most 16 valid string pairs")
		}
		filters[key] = values[0]
	}
	return filters, nil
}

func singleChatQueryValues(request *http.Request) (string, string, error) {
	query := request.URL.Query()
	for _, name := range []string{"model", "after"} {
		if len(query[name]) > 1 {
			return "", "", errors.New(name + " must be specified once")
		}
	}
	return query.Get("model"), query.Get("after"), nil
}

func chatCompletionPredicate(keyID string, now int64, model string, filters map[string]string) (string, []any) {
	predicate := `c.key_id=? AND c.expires_at>?`
	arguments := []any{keyID, now}
	if model != "" {
		predicate += ` AND c.model_id=?`
		arguments = append(arguments, model)
	}
	filterKeys := make([]string, 0, len(filters))
	for key := range filters {
		filterKeys = append(filterKeys, key)
	}
	sort.Strings(filterKeys)
	for _, key := range filterKeys {
		predicate += ` AND EXISTS (SELECT 1 FROM stored_chat_completion_metadata m WHERE m.completion_id=c.id AND m.metadata_key=? AND m.metadata_value=?)`
		arguments = append(arguments, key, filters[key])
	}
	return predicate, arguments
}

func replaceStoredChatMetadata(ctx context.Context, tx *sql.Tx, completionID string, raw json.RawMessage) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM stored_chat_completion_metadata WHERE completion_id=?`, completionID); err != nil {
		return err
	}
	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stored_chat_completion_metadata(completion_id,metadata_key,metadata_value) VALUES(?,?,?)`, completionID, key, metadata[key]); err != nil {
			return err
		}
	}
	return nil
}

func (handler *Handler) listChatCompletionMessages(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.chatPrincipal(response, request)
	if !ok {
		return
	}
	limit, order, err := pageOptions(request, "asc")
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := request.PathValue("completion_id")
	var body []byte
	err = handler.database.QueryRowContext(request.Context(), `SELECT request_json FROM stored_chat_completions WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Chat Completion not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completion messages are unavailable")
		return
	}
	items, err := storedChatMessages(id, body)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Chat Completion messages are unavailable")
		return
	}
	if order == "DESC" {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	start := 0
	if after := request.URL.Query().Get("after"); after != "" {
		start = -1
		for index, item := range items {
			var value map[string]json.RawMessage
			_ = json.Unmarshal(item, &value)
			var itemID string
			_ = json.Unmarshal(value["id"], &itemID)
			if itemID == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after is not a valid message cursor")
			return
		}
	}
	end := min(start+limit, len(items))
	writeCursorPage(response, items[start:end], end < len(items))
}

func storedChatMessages(completionID string, requestBody []byte) ([]json.RawMessage, error) {
	var request map[string]json.RawMessage
	var messages []json.RawMessage
	if json.Unmarshal(requestBody, &request) != nil || json.Unmarshal(request["messages"], &messages) != nil {
		return nil, errors.New("invalid stored messages")
	}
	for index, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil {
			return nil, errors.New("invalid stored message")
		}
		message["id"], _ = json.Marshal(completionID + "-" + strconv.Itoa(index))
		if _, exists := message["name"]; !exists {
			message["name"] = json.RawMessage(`null`)
		}
		var parts []json.RawMessage
		if json.Unmarshal(message["content"], &parts) == nil {
			message["content_parts"] = message["content"]
			message["content"] = json.RawMessage(`null`)
		} else {
			message["content_parts"] = json.RawMessage(`null`)
		}
		messages[index], _ = json.Marshal(message)
	}
	return messages, nil
}

func pageOptions(request *http.Request, defaultOrder string) (int, string, error) {
	query := request.URL.Query()
	for _, name := range []string{"limit", "order"} {
		if len(query[name]) > 1 {
			return 0, "", errors.New(name + " must be specified once")
		}
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return 0, "", errors.New("limit must be between 1 and 100")
		}
	}
	order := query.Get("order")
	if order == "" {
		order = defaultOrder
	}
	if order != "asc" && order != "desc" {
		return 0, "", errors.New("order must be asc or desc")
	}
	return limit, strings.ToUpper(order), nil
}

func writeCursorPage(response http.ResponseWriter, data []json.RawMessage, more bool) {
	result := map[string]any{"object": "list", "data": data, "has_more": more, "first_id": "", "last_id": ""}
	if len(data) > 0 {
		var first, last map[string]json.RawMessage
		_ = json.Unmarshal(data[0], &first)
		_ = json.Unmarshal(data[len(data)-1], &last)
		var firstID, lastID string
		_ = json.Unmarshal(first["id"], &firstID)
		_ = json.Unmarshal(last["id"], &lastID)
		result["first_id"], result["last_id"] = firstID, lastID
	}
	writeJSON(response, result)
}

func (handler *Handler) chatPrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "openai")
	if ok && !principalHasScope(principal.Scopes, "chat:generate") {
		handler.writeError(response, "openai", http.StatusForbidden, "permission_denied", "Chat Completions access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) chatRequest(response http.ResponseWriter, request *http.Request) (keys.Principal, []byte, bool) {
	principal, ok := handler.chatPrincipal(response, request)
	if !ok {
		return keys.Principal{}, nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return keys.Principal{}, nil, false
	}
	return principal, body, true
}
