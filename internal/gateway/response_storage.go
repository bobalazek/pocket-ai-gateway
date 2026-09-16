package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	storedResponseLifetime = 30 * 24 * time.Hour
	retainedResponseJobs   = 10000
	retainedResponseBytes  = 1 << 30
	retainedOwnerJobs      = 2500
	retainedOwnerBytes     = 512 << 20
	retainedKeyJobs        = 250
	retainedKeyBytes       = 128 << 20
)

var errRetainedResourceLimit = errors.New("retained inference resource limit reached")

type preparedResponse struct {
	id        string
	body      []byte
	createdAt time.Time
	state     string
}

func prepareStoredResponse(modelID string, body []byte) (preparedResponse, error) {
	token, err := credentials.RandomToken(18)
	if err != nil {
		return preparedResponse{}, err
	}
	now := time.Now()
	id := "resp_" + token
	encoded, err := rewriteResponse(id, modelID, false, now, body)
	return preparedResponse{id: id, body: encoded, createdAt: now, state: "completed"}, err
}

func rewriteResponse(id, modelID string, background bool, createdAt time.Time, body []byte) ([]byte, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return nil, errors.New("provider response is not a JSON object")
	}
	var object, status string
	if json.Unmarshal(value["object"], &object) != nil || object != "response" || json.Unmarshal(value["status"], &status) != nil || status == "" {
		return nil, errors.New("provider response is not a valid Response object")
	}
	value["id"], _ = json.Marshal(id)
	value["model"], _ = json.Marshal(modelID)
	value["object"] = json.RawMessage(`"response"`)
	value["store"] = json.RawMessage(`true`)
	value["background"] = json.RawMessage(strconv.FormatBool(background))
	if background || len(value["created_at"]) == 0 {
		value["created_at"] = json.RawMessage(strconv.FormatInt(createdAt.Unix(), 10))
	}
	return json.Marshal(value)
}

func (handler *Handler) storeResponse(ctx context.Context, requestID, ownerID, keyID, modelID string, requestBody []byte, value preparedResponse, attachment *conversationAttachment, requestState string) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkRetainedResourceCapacity(ctx, tx, ownerID, keyID, 1, int64(len(requestBody)+len(value.body))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json) VALUES(?,?,?,?,?,?,?,?,?)`, value.id, ownerID, keyID, modelID, value.body, value.createdAt.UnixMilli(), value.createdAt.Add(storedResponseLifetime).UnixMilli(), value.state, requestBody); err != nil {
		return err
	}
	if attachment != nil {
		if err := appendConversationTurn(ctx, tx, *attachment, value.createdAt.UnixMilli()); err != nil {
			return err
		}
	}
	if err := handler.usage.FinalizeRequestTx(ctx, tx, requestID, requestState); err != nil {
		return err
	}
	return tx.Commit()
}

type responseQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkRetainedResourceCapacity(ctx context.Context, query responseQueryer, ownerID, keyID string, incomingCount, incomingBytes int64) error {
	var count, size, ownerCount, ownerSize, keyCount, keySize int64
	err := query.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0),
		COALESCE(SUM(CASE WHEN owner_user_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN size ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN key_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN key_id=? THEN size ELSE 0 END),0)
		FROM (
			SELECT owner_user_id,key_id,COALESCE(length(request_json),0)+length(body_json)+COALESCE(length(conversation_items_json),0)+CASE WHEN state IN ('queued','running') THEN ? ELSE 0 END AS size FROM stored_responses WHERE expires_at>?
			UNION ALL
			SELECT owner_user_id,key_id,length(request_json)+length(body_json)+length(metadata_json) AS size FROM stored_chat_completions WHERE expires_at>?
			UNION ALL
			SELECT message_batches.owner_user_id,message_batches.key_id,length(message_batch_items.params_json)+CASE WHEN message_batch_items.state IN ('queued','claimed','dispatching','settling') THEN MAX(message_batch_items.reserved_result_bytes,COALESCE(length(message_batch_items.result_json),0)) ELSE COALESCE(length(message_batch_items.result_json),0) END AS size
			FROM message_batch_items JOIN message_batches ON message_batches.id=message_batch_items.batch_id WHERE message_batches.expires_at>?
			UNION ALL
			SELECT owner_user_id,key_id,length(filename)+length(ciphertext)+length(nonce) AS size FROM openai_files WHERE expires_at>?
			UNION ALL
			SELECT owner_user_id,key_id,length(filename)+length(mime_type)+expected_bytes+28 AS size FROM openai_uploads WHERE status='pending' AND expires_at>?
			UNION ALL
			SELECT owner_user_id,key_id,length(input_file_id)+length(endpoint)+length(completion_window)+length(model_id)+length(metadata_json)+COALESCE(length(output_file_id),0)+COALESCE(length(error_file_id),0) AS size FROM openai_batches WHERE retention_expires_at>?
			UNION ALL
			SELECT openai_batches.owner_user_id,openai_batches.key_id,length(openai_batch_items.request_ciphertext)+length(openai_batch_items.request_nonce)+CASE WHEN openai_batch_items.state IN ('queued','claimed','dispatching','settling') THEN MAX(openai_batch_items.reserved_result_bytes,COALESCE(length(openai_batch_items.result_ciphertext),0)+COALESCE(length(openai_batch_items.result_nonce),0)) ELSE COALESCE(length(openai_batch_items.result_ciphertext),0)+COALESCE(length(openai_batch_items.result_nonce),0) END AS size
			FROM openai_batch_items JOIN openai_batches ON openai_batches.id=openai_batch_items.batch_id WHERE openai_batches.retention_expires_at>?
			UNION ALL
			SELECT owner_user_id,key_id,length(name)+length(description)+length(metadata_json) AS size FROM openai_vector_stores WHERE expires_at IS NULL OR expires_at>?
			UNION ALL
			SELECT openai_vector_stores.owner_user_id,openai_vector_stores.key_id,length(openai_vector_store_files.file_id)+length(openai_vector_store_files.attributes_json)+length(openai_vector_store_files.chunking_strategy_json) AS size
			FROM openai_vector_store_files
			JOIN openai_vector_stores ON openai_vector_stores.id=openai_vector_store_files.vector_store_id
			JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id
			WHERE (openai_vector_stores.expires_at IS NULL OR openai_vector_stores.expires_at>?) AND openai_files.expires_at>?
		)`, ownerID, ownerID, keyID, keyID, maxInferenceBody, time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli()).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
	if err != nil {
		return err
	}
	if count+incomingCount > retainedResponseJobs || size+incomingBytes > retainedResponseBytes || ownerCount+incomingCount > retainedOwnerJobs || ownerSize+incomingBytes > retainedOwnerBytes || keyCount+incomingCount > retainedKeyJobs || keySize+incomingBytes > retainedKeyBytes {
		return errRetainedResourceLimit
	}
	return nil
}

func (handler *Handler) getResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	var body []byte
	err := handler.database.QueryRowContext(request.Context(), `SELECT body_json FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, request.PathValue("response_id"), principal.KeyID, time.Now().UnixMilli()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response is unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) deleteResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("response_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	handler.cancelActive(id)
	writeJSON(response, map[string]any{"id": id, "object": "response", "deleted": true})
}

func (handler *Handler) cancelResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id, now := request.PathValue("response_id"), time.Now()
	var modelID, state string
	var createdAt int64
	var conversationID sql.NullString
	err := handler.database.QueryRowContext(request.Context(), `SELECT model_id,state,created_at,conversation_id FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, now.UnixMilli()).Scan(&modelID, &state, &createdAt, &conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response is unavailable")
		return
	}
	if state != "queued" && state != "running" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Only queued or in-progress Responses can be cancelled")
		return
	}
	body := responseState(id, modelID, "cancelled", time.UnixMilli(createdAt), nil, conversationID.String)
	result, err := handler.database.ExecContext(request.Context(), `UPDATE stored_responses SET state='cancelled',body_json=?,cancel_requested=1,finished_at=?,conversation_items_json=NULL WHERE id=? AND key_id=? AND state IN ('queued','running')`, body, now.UnixMilli(), id, principal.KeyID)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be cancelled")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "responses", http.StatusConflict, "conflict", "Response state changed")
		return
	}
	handler.cancelActive(id)
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) responseInputItems(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("response_id")
	var raw []byte
	err := handler.database.QueryRowContext(request.Context(), `SELECT request_json FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response input is unavailable")
		return
	}
	if len(raw) == 0 {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "Input items are unavailable for Responses created before this gateway version")
		return
	}
	if len(request.URL.Query()["include"]) > 0 || len(request.URL.Query()["include[]"]) > 0 {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "include projections are not supported for input items")
		return
	}
	items, err := inputItems(id, raw)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response input is unavailable")
		return
	}
	if order := request.URL.Query().Get("order"); order == "" || order == "desc" {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	} else if order != "asc" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "order must be asc or desc")
		return
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
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "after is not a valid input item cursor")
			return
		}
	}
	limit := 20
	if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 100 {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
	}
	end := min(start+limit, len(items))
	page := items[start:end]
	result := map[string]any{"object": "list", "data": page, "has_more": end < len(items)}
	if len(page) > 0 {
		var first, last map[string]json.RawMessage
		_ = json.Unmarshal(page[0], &first)
		_ = json.Unmarshal(page[len(page)-1], &last)
		var firstID, lastID string
		_ = json.Unmarshal(first["id"], &firstID)
		_ = json.Unmarshal(last["id"], &lastID)
		result["first_id"], result["last_id"] = firstID, lastID
	}
	writeJSON(response, result)
}

func inputItems(responseID string, requestBody []byte) ([]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(requestBody, &envelope) != nil {
		return nil, errors.New("invalid stored input")
	}
	raw := envelope["input"]
	var text string
	var items []json.RawMessage
	if json.Unmarshal(raw, &text) == nil {
		content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": text}})
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": json.RawMessage(content)})
		items = []json.RawMessage{item}
	} else if json.Unmarshal(raw, &items) != nil {
		return nil, errors.New("invalid stored input")
	}
	for index, item := range items {
		var value map[string]json.RawMessage
		if json.Unmarshal(item, &value) != nil {
			return nil, errors.New("invalid stored input item")
		}
		var existing string
		_ = json.Unmarshal(value["id"], &existing)
		if !strings.HasPrefix(existing, "citem_") {
			sum := sha256.Sum256([]byte(responseID + ":" + strconv.Itoa(index)))
			value["id"], _ = json.Marshal("item_" + hex.EncodeToString(sum[:12]))
		}
		items[index], _ = json.Marshal(value)
	}
	return items, nil
}

func (handler *Handler) responsePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "responses")
	if ok && !principalHasScope(principal.Scopes, "responses:generate") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func principalHasScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func responseState(id, modelID, status string, createdAt time.Time, responseError map[string]string, conversationID ...string) []byte {
	value := map[string]any{
		"id": id, "object": "response", "created_at": createdAt.Unix(), "status": status,
		"background": true, "store": true, "model": modelID, "output": []any{},
		"error": responseError, "incomplete_details": nil, "usage": nil,
	}
	if len(conversationID) > 0 && conversationID[0] != "" {
		value["conversation"] = map[string]string{"id": conversationID[0]}
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// RunBackground processes durable Responses jobs until ctx is cancelled.
