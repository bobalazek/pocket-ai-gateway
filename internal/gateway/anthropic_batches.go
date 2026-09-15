package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	messageBatchMaxItems           = 4
	messageBatchProcessingLifetime = 24 * time.Hour
	messageBatchResultsLifetime    = 29 * 24 * time.Hour
)

var messageBatchCustomID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type messageBatchRow struct {
	id, status                                                string
	cancelRequested                                           bool
	createdAt, endedAt, cancelInitiatedAt, retentionExpiresAt int64
}

type messageBatchRequest struct {
	CustomID string          `json:"custom_id"`
	Params   json.RawMessage `json:"params"`
}

func (handler *Handler) messageBatchPrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "anthropic")
	if !ok {
		return keys.Principal{}, false
	}
	if !principalHasScope(principal.Scopes, "messages:batches") {
		handler.writeError(response, "anthropic", http.StatusForbidden, "permission_error", "Message Batches access is not permitted")
		return keys.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) createMessageBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	if !principalHasScope(principal.Scopes, "chat:generate") {
		handler.writeError(response, "anthropic", http.StatusForbidden, "permission_error", "Message creation access is not permitted")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody+1))
	if err != nil || len(body) > maxInferenceBody {
		handler.writeError(response, "anthropic", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil || len(envelope) != 1 {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Request body must contain only requests")
		return
	}
	var requests []messageBatchRequest
	decoder := json.NewDecoder(bytes.NewReader(envelope["requests"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&requests) != nil || len(requests) < 1 || len(requests) > messageBatchMaxItems {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "requests must contain between 1 and 4 items")
		return
	}
	seen := map[string]struct{}{}
	queueBytes := int64(len(body))
	incomingBytes := queueBytes
	for _, item := range requests {
		if !messageBatchCustomID.MatchString(item.CustomID) {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "custom_id must contain 1 to 64 letters, numbers, underscores, or hyphens")
			return
		}
		if _, exists := seen[item.CustomID]; exists {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "custom_id values must be unique")
			return
		}
		seen[item.CustomID] = struct{}{}
		if err := validateMessageBatchParamsObject(item.Params); err != nil {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		incomingBytes += maxInferenceBody
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be created")
		return
	}
	now := time.Now()
	batch := messageBatchRow{id: "msgbatch_" + token, status: "in_progress", createdAt: now.UnixMilli(), retentionExpiresAt: now.Add(messageBatchResultsLifetime).UnixMilli()}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		err = checkBackgroundQueueCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, int64(len(requests)), queueBytes)
	}
	if err == nil {
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, int64(len(requests)), incomingBytes)
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO message_batches(id,owner_user_id,key_id,processing_status,cancel_requested,created_at,expires_at) VALUES(?,?,?,'in_progress',0,?,?)`, batch.id, principal.OwnerUserID, principal.KeyID, batch.createdAt, batch.retentionExpiresAt)
	}
	for index, item := range requests {
		if err != nil {
			break
		}
		_, err = tx.ExecContext(request.Context(), `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,state,reserved_result_bytes) VALUES(?,?,?,?,'queued',?)`, batch.id, index+1, item.CustomID, []byte(item.Params), maxInferenceBody)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) || errors.Is(err, errBackgroundQueueLimit) {
			handler.writeError(response, "anthropic", http.StatusTooManyRequests, "rate_limit_error", "Message Batch capacity is exhausted")
			return
		}
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be created")
		return
	}
	select {
	case handler.wake <- struct{}{}:
	default:
	}
	handler.writeMessageBatch(response, request, batch)
}

func validateMessageBatchParams(raw json.RawMessage) error {
	if err := validateMessageBatchParamsObject(raw); err != nil {
		return err
	}
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	var model string
	if json.Unmarshal(value["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return errors.New("each params value requires a model")
	}
	var maxTokens int64
	if json.Unmarshal(value["max_tokens"], &maxTokens) != nil || maxTokens < 1 {
		return errors.New("each params value requires max_tokens as a positive integer")
	}
	var messages []json.RawMessage
	if json.Unmarshal(value["messages"], &messages) != nil || len(messages) == 0 {
		return errors.New("each params value requires at least one message")
	}
	if stream, exists := value["stream"]; exists && !bytes.Equal(bytes.TrimSpace(stream), []byte("null")) {
		var enabled bool
		if json.Unmarshal(stream, &enabled) != nil || enabled {
			return errors.New("streaming is not supported in Message Batches")
		}
	}
	return nil
}

func validateMessageBatchParamsObject(raw json.RawMessage) error {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return errors.New("each params value must be a JSON object")
	}
	return nil
}

func (handler *Handler) getMessageBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	batch, err := handler.loadMessageBatch(request.Context(), principal.KeyID, request.PathValue("message_batch_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "Message Batch not found")
		return
	}
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch is unavailable")
		return
	}
	handler.writeMessageBatch(response, request, batch)
}

func (handler *Handler) listMessageBatches(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	limit := 20
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "limit must be between 1 and 1000")
			return
		}
		limit = parsed
	}
	after, before := request.URL.Query().Get("after_id"), request.URL.Query().Get("before_id")
	if after != "" && before != "" {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "after_id and before_id cannot be combined")
		return
	}
	var cursorCreated int64
	if cursor := firstNonempty(after, before); cursor != "" {
		if err := handler.database.QueryRowContext(request.Context(), `SELECT created_at FROM message_batches WHERE id=? AND key_id=?`, cursor, principal.KeyID).Scan(&cursorCreated); errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Invalid Message Batch cursor")
			return
		} else if err != nil {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batches are unavailable")
			return
		}
	}
	condition, args := "", []any{principal.KeyID}
	if after != "" {
		condition = " AND (created_at < ? OR (created_at = ? AND id < ?))"
		args = append(args, cursorCreated, cursorCreated, after)
	}
	if before != "" {
		condition = " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, cursorCreated, cursorCreated, before)
	}
	order := "DESC"
	if before != "" {
		order = "ASC"
	}
	args = append(args, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), `SELECT id,processing_status,cancel_requested,created_at,ended_at,cancel_initiated_at,expires_at FROM message_batches WHERE key_id=?`+condition+` ORDER BY created_at `+order+`,id `+order+` LIMIT ?`, args...)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batches are unavailable")
		return
	}
	defer rows.Close()
	batches := make([]messageBatchRow, 0, limit+1)
	for rows.Next() {
		batch, scanErr := scanMessageBatch(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		batches = append(batches, batch)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batches are unavailable")
		return
	}
	hasMore := len(batches) > limit
	if hasMore {
		batches = batches[:limit]
	}
	if before != "" {
		for left, right := 0, len(batches)-1; left < right; left, right = left+1, right-1 {
			batches[left], batches[right] = batches[right], batches[left]
		}
	}
	data := make([]any, 0, len(batches))
	for _, batch := range batches {
		value, valueErr := handler.messageBatchValue(request.Context(), request, batch)
		if valueErr != nil {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batches are unavailable")
			return
		}
		data = append(data, value)
	}
	var firstID, lastID any
	if len(batches) > 0 {
		firstID, lastID = batches[0].id, batches[len(batches)-1].id
	}
	writeBatchJSON(response, http.StatusOK, map[string]any{"data": data, "has_more": hasMore, "first_id": firstID, "last_id": lastID})
}

func (handler *Handler) cancelMessageBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("message_batch_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be canceled")
		return
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(request.Context(), `SELECT processing_status FROM message_batches WHERE id=? AND key_id=?`, id, principal.KeyID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "Message Batch not found")
		return
	} else if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be canceled")
		return
	}
	if status != "ended" {
		_, err = tx.ExecContext(request.Context(), `UPDATE message_batches SET processing_status='canceling',cancel_requested=1,cancel_initiated_at=COALESCE(cancel_initiated_at,?) WHERE id=? AND key_id=? AND processing_status<>'ended'`, time.Now().UnixMilli(), id, principal.KeyID)
		if err == nil {
			err = cancelQueuedBatchItems(request.Context(), tx, id, "canceled")
		}
		if err == nil {
			err = finishMessageBatchTx(request.Context(), tx, id)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be canceled")
		return
	}
	handler.cancelBatchActive(id)
	batch, err := handler.loadMessageBatch(request.Context(), principal.KeyID, id)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch is unavailable")
		return
	}
	handler.writeMessageBatch(response, request, batch)
}

func (handler *Handler) deleteMessageBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("message_batch_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM message_batches WHERE id=? AND key_id=? AND processing_status='ended'`, id, principal.KeyID)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be deleted")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be deleted")
		return
	}
	if changed == 0 {
		var exists int
		err = handler.database.QueryRowContext(request.Context(), `SELECT 1 FROM message_batches WHERE id=? AND key_id=?`, id, principal.KeyID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "Message Batch not found")
		} else if err != nil {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch could not be deleted")
		} else {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Only ended Message Batches can be deleted")
		}
		return
	}
	writeBatchJSON(response, http.StatusOK, map[string]any{"id": id, "type": "message_batch_deleted"})
}

func (handler *Handler) messageBatchResults(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.messageBatchPrincipal(response, request)
	if !ok {
		return
	}
	batch, err := handler.loadMessageBatch(request.Context(), principal.KeyID, request.PathValue("message_batch_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "Message Batch not found")
		return
	}
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch results are unavailable")
		return
	}
	if batch.status != "ended" {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Message Batch results are not ready")
		return
	}
	rows, err := handler.database.QueryContext(request.Context(), `SELECT result_json FROM message_batch_items WHERE batch_id=? ORDER BY ordinal`, batch.id)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch results are unavailable")
		return
	}
	defer rows.Close()
	var output bytes.Buffer
	for rows.Next() {
		var line []byte
		if rows.Scan(&line) != nil || !json.Valid(line) || output.Len()+len(line)+1 > messageBatchMaxItems*(maxInferenceBody+1024) {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch results are unavailable")
			return
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.Write(line)
	}
	if rows.Err() != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch results are unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/x-jsonlines")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(output.Bytes())
}

func (handler *Handler) loadMessageBatch(ctx context.Context, keyID, id string) (messageBatchRow, error) {
	return scanMessageBatch(handler.database.QueryRowContext(ctx, `SELECT id,processing_status,cancel_requested,created_at,ended_at,cancel_initiated_at,expires_at FROM message_batches WHERE id=? AND key_id=?`, id, keyID))
}

type messageBatchScanner interface{ Scan(...any) error }

func scanMessageBatch(row messageBatchScanner) (messageBatchRow, error) {
	var value messageBatchRow
	var ended, canceled sql.NullInt64
	err := row.Scan(&value.id, &value.status, &value.cancelRequested, &value.createdAt, &ended, &canceled, &value.retentionExpiresAt)
	if ended.Valid {
		value.endedAt = ended.Int64
	}
	if canceled.Valid {
		value.cancelInitiatedAt = canceled.Int64
	}
	return value, err
}

func (handler *Handler) writeMessageBatch(response http.ResponseWriter, request *http.Request, batch messageBatchRow) {
	value, err := handler.messageBatchValue(request.Context(), request, batch)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "gateway_unavailable", "Message Batch is unavailable")
		return
	}
	writeBatchJSON(response, http.StatusOK, value)
}

func (handler *Handler) messageBatchValue(ctx context.Context, request *http.Request, batch messageBatchRow) (map[string]any, error) {
	counts := map[string]int64{"processing": 0, "succeeded": 0, "errored": 0, "canceled": 0, "expired": 0}
	rows, err := handler.database.QueryContext(ctx, `SELECT state,COUNT(*) FROM message_batch_items WHERE batch_id=? GROUP BY state`, batch.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		total += count
		if batch.status != "ended" {
			continue
		}
		switch state {
		case "queued", "claimed", "dispatching", "settling":
			counts["processing"] += count
		case "succeeded":
			counts["succeeded"] += count
		case "canceled":
			counts["canceled"] += count
		case "expired":
			counts["expired"] += count
		default:
			counts["errored"] += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if batch.status != "ended" {
		counts["processing"] = total
	}
	var endedAt, cancelAt, resultsURL any
	if batch.endedAt > 0 {
		endedAt = formatBatchTime(batch.endedAt)
	}
	if batch.cancelInitiatedAt > 0 {
		cancelAt = formatBatchTime(batch.cancelInitiatedAt)
	}
	if batch.status == "ended" {
		resultsURL = handler.batchOrigin(request) + "/api/anthropic/v1/messages/batches/" + url.PathEscape(batch.id) + "/results"
	}
	return map[string]any{"id": batch.id, "type": "message_batch", "processing_status": batch.status, "request_counts": counts, "ended_at": endedAt, "created_at": formatBatchTime(batch.createdAt), "expires_at": formatBatchTime(batch.createdAt + messageBatchProcessingLifetime.Milliseconds()), "archived_at": nil, "cancel_initiated_at": cancelAt, "results_url": resultsURL}, nil
}

func (handler *Handler) batchOrigin(request *http.Request) string {
	if handler.publicOrigin != "" {
		return handler.publicOrigin
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host
}

func formatBatchTime(value int64) string { return time.UnixMilli(value).UTC().Format(time.RFC3339Nano) }
func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func writeBatchJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
