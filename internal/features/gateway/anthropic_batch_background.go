package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type messageBatchItemContextKey struct{}

var errMessageBatchQueueLimit = errors.New("message batch queue limit reached")

type messageBatchJob struct {
	batchID, ownerID, keyID, customID string
	ordinal, size                     int64
	createdAt                         int64
	params                            []byte
}

func checkMessageBatchQueueCapacity(ctx context.Context, query responseQueryer, ownerID, keyID string, incomingCount, incomingBytes int64) error {
	var count, size, ownerCount, ownerSize, keyCount, keySize int64
	err := query.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0),
		COALESCE(SUM(CASE WHEN owner_user_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN size ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN key_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN key_id=? THEN size ELSE 0 END),0)
		FROM (
			SELECT owner_user_id,key_id,length(request_json)+length(body_json)+COALESCE(length(conversation_items_json),0) AS size FROM stored_responses WHERE state IN ('queued','running')
			UNION ALL
			SELECT b.owner_user_id,b.key_id,length(i.params_json)+COALESCE(length(i.result_json),0) AS size FROM message_batch_items i JOIN message_batches b ON b.id=i.batch_id WHERE i.state IN ('queued','claimed','dispatching','settling')
		)`, ownerID, ownerID, keyID, keyID).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
	if err != nil {
		return err
	}
	if count+incomingCount > backgroundQueueJobs || size+incomingBytes > backgroundQueueBytes || ownerCount+incomingCount > backgroundOwnerJobs || ownerSize+incomingBytes > backgroundOwnerBytes || keyCount+incomingCount > backgroundKeyJobs || keySize+incomingBytes > backgroundKeyBytes {
		return errMessageBatchQueueLimit
	}
	return nil
}

func (handler *Handler) recoverMessageBatches(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,i.state,i.result_json FROM message_batch_items i JOIN message_batches b ON b.id=i.batch_id WHERE i.state IN ('claimed','dispatching','settling')`)
	if err != nil {
		return err
	}
	type item struct {
		batchID, customID, state string
		ordinal                  int64
		result                   []byte
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.batchID, &value.ordinal, &value.customID, &value.state, &value.result); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		if item.state == "claimed" {
			if _, err := handler.database.ExecContext(ctx, `UPDATE message_batch_items SET state='queued',lease_epoch='',claimed_at=NULL WHERE batch_id=? AND ordinal=? AND state='claimed'`, item.batchID, item.ordinal); err != nil {
				return err
			}
			continue
		}
		if item.state == "settling" {
			terminal := messageBatchResultState(item.result)
			if _, err := handler.database.ExecContext(ctx, `UPDATE message_batch_items SET state=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state='settling'`, terminal, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
				return err
			}
			continue
		}
		line := messageBatchErrorLine(item.customID, "api_error", "The gateway restarted while the provider outcome was unknown.")
		if _, err := handler.database.ExecContext(ctx, `UPDATE message_batch_items SET state='interrupted_unknown',result_json=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('dispatching','settling')`, line, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
			return err
		}
	}
	return handler.reconcileMessageBatches(ctx)
}

func (handler *Handler) claimMessageBatch(ctx context.Context) (messageBatchJob, bool) {
	if err := handler.reconcileMessageBatches(ctx); err != nil {
		return messageBatchJob{}, false
	}
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return messageBatchJob{}, false
	}
	defer tx.Rollback()
	var job messageBatchJob
	err = tx.QueryRowContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,i.params_json,b.owner_user_id,b.key_id,(SELECT COUNT(*) FROM message_batch_items x WHERE x.batch_id=i.batch_id),b.created_at FROM message_batch_items i JOIN message_batches b ON b.id=i.batch_id WHERE i.state='queued' AND b.processing_status='in_progress' AND b.cancel_requested=0 AND b.created_at>? ORDER BY b.created_at,i.ordinal LIMIT 1`, time.Now().Add(-messageBatchProcessingLifetime).UnixMilli()).Scan(&job.batchID, &job.ordinal, &job.customID, &job.params, &job.ownerID, &job.keyID, &job.size, &job.createdAt)
	if err != nil {
		return messageBatchJob{}, false
	}
	result, err := tx.ExecContext(ctx, `UPDATE message_batch_items SET state='claimed',lease_epoch=?,claimed_at=? WHERE batch_id=? AND ordinal=? AND state='queued'`, handler.epoch, time.Now().UnixMilli(), job.batchID, job.ordinal)
	if err != nil {
		return messageBatchJob{}, false
	}
	changed, _ := result.RowsAffected()
	if changed != 1 || tx.Commit() != nil {
		return messageBatchJob{}, false
	}
	return job, true
}

func (handler *Handler) runMessageBatch(ctx context.Context, job messageBatchJob) {
	principal, err := handler.keys.Principal(ctx, job.keyID)
	if err != nil || principal.OwnerUserID != job.ownerID || !principalHasScope(principal.Scopes, "messages:batches") || !principalHasScope(principal.Scopes, "chat:generate") {
		handler.finishMessageBatchItem(ctx, job, "errored", messageBatchErrorLine(job.customID, "permission_error", "The creating API key or its grants are no longer active."), "", "")
		return
	}
	if err := validateMessageBatchParams(job.params); err != nil {
		handler.finishMessageBatchItem(ctx, job, "errored", messageBatchErrorLine(job.customID, "invalid_request_error", err.Error()), "", "")
		return
	}
	if !handler.transitionBackground(ctx, `UPDATE message_batch_items SET state='dispatching',dispatch_started_at=? WHERE batch_id=? AND ordinal=? AND state='claimed' AND lease_epoch=?`, time.Now().UnixMilli(), job.batchID, job.ordinal, handler.epoch) {
		return
	}
	jobID := fmt.Sprintf("%s:%d", job.batchID, job.ordinal)
	deadline := time.UnixMilli(job.createdAt).Add(messageBatchProcessingLifetime)
	jobContext, cancel := context.WithDeadline(ctx, deadline)
	handler.activeMu.Lock()
	handler.active[jobID] = cancel
	handler.activeMu.Unlock()
	defer func() {
		cancel()
		handler.activeMu.Lock()
		delete(handler.active, jobID)
		handler.activeMu.Unlock()
	}()
	var canceled bool
	cancelReadErr := handler.database.QueryRowContext(ctx, `SELECT cancel_requested FROM message_batches WHERE id=?`, job.batchID).Scan(&canceled)
	if ctx.Err() != nil {
		return
	}
	if canceled {
		handler.finishMessageBatchItem(ctx, job, "canceled", messageBatchSimpleLine(job.customID, "canceled"), "", "")
		return
	}
	if errors.Is(jobContext.Err(), context.DeadlineExceeded) || !time.Now().Before(deadline) {
		handler.finishMessageBatchItem(ctx, job, "expired", messageBatchSimpleLine(job.customID, "expired"), "", "")
		return
	}
	if errors.Is(jobContext.Err(), context.Canceled) {
		handler.finishMessageBatchItem(ctx, job, "canceled", messageBatchSimpleLine(job.customID, "canceled"), "", "")
		return
	}
	if cancelReadErr != nil {
		handler.finishMessageBatchItem(ctx, job, "errored", messageBatchErrorLine(job.customID, "api_error", "Message Batch cancellation state could not be checked."), "", "")
		return
	}
	recorder := &memoryResponse{header: make(http.Header)}
	request, _ := http.NewRequestWithContext(context.WithValue(jobContext, messageBatchItemContextKey{}, job.size), http.MethodPost, "/api/anthropic/v1/messages", bytes.NewReader(job.params))
	request.Header.Set("anthropic-version", "2023-06-01")
	handler.forwardAuthorized(recorder, request, "anthropic", "chat:generate", "messages", "", nil, principal, job.params)
	if ctx.Err() != nil {
		return
	}
	for recorder.attemptID != "" && recorder.settlementErr != nil && !permanentSettlementError(recorder.settlementErr) {
		recorder.settlementErr = handler.settle(recorder.attemptID, recorder.settlement)
		if recorder.settlementErr != nil && !waitBackground(ctx, time.Second) {
			return
		}
	}
	completedOutcome := recorder.attemptID != "" && recorder.settlementErr == nil && (recorder.settlement.State == "succeeded" || recorder.settlement.State == "failed")
	_ = handler.database.QueryRowContext(ctx, `SELECT cancel_requested FROM message_batches WHERE id=?`, job.batchID).Scan(&canceled)
	if !completedOutcome && canceled {
		handler.finishMessageBatchItem(ctx, job, "canceled", messageBatchSimpleLine(job.customID, "canceled"), recorder.requestID, recorder.attemptID)
		return
	}
	if !completedOutcome && !time.Now().Before(deadline) {
		handler.finishMessageBatchItem(ctx, job, "expired", messageBatchSimpleLine(job.customID, "expired"), recorder.requestID, recorder.attemptID)
		return
	}
	if !completedOutcome && errors.Is(jobContext.Err(), context.Canceled) {
		handler.finishMessageBatchItem(ctx, job, "canceled", messageBatchSimpleLine(job.customID, "canceled"), recorder.requestID, recorder.attemptID)
		return
	}
	state := "errored"
	result := messageBatchErroredLine(job.customID, recorder.body.Bytes())
	if completedOutcome && recorder.settlement.State == "succeeded" && recorder.status >= 200 && recorder.status < 300 && validAnthropicBatchMessage(recorder.body.Bytes()) {
		state, result = "succeeded", messageBatchSuccessLine(job.customID, recorder.body.Bytes())
	}
	state, result = boundMessageBatchResult(job.customID, state, result)
	if recorder.requestID != "" {
		if !handler.transitionBackground(ctx, `UPDATE message_batch_items SET state='settling',result_json=?,request_id=?,attempt_id=? WHERE batch_id=? AND ordinal=? AND state='dispatching' AND lease_epoch=?`, result, recorder.requestID, nullIfEmpty(recorder.attemptID), job.batchID, job.ordinal, handler.epoch) {
			return
		}
	}
	handler.finishMessageBatchItem(ctx, job, state, result, recorder.requestID, recorder.attemptID)
}

func (handler *Handler) finishMessageBatchItem(ctx context.Context, job messageBatchJob, state string, result []byte, requestID, attemptID string) {
	state, result = boundMessageBatchResult(job.customID, state, result)
	for ctx.Err() == nil {
		tx, err := handler.database.BeginTx(ctx, nil)
		if err == nil {
			updated, updateErr := tx.ExecContext(ctx, `UPDATE message_batch_items SET state=?,result_json=?,request_id=COALESCE(NULLIF(?,''),request_id),attempt_id=COALESCE(NULLIF(?,''),attempt_id),finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('claimed','dispatching','settling')`, state, result, requestID, attemptID, time.Now().UnixMilli(), job.batchID, job.ordinal)
			err = updateErr
			if err == nil {
				changed, rowsErr := updated.RowsAffected()
				if rowsErr != nil {
					err = rowsErr
				} else if changed != 1 {
					_ = tx.Rollback()
					return
				}
			}
			if err == nil {
				err = finishMessageBatchTx(ctx, tx, job.batchID)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err == nil {
			return
		}
		if !waitBackground(ctx, 250*time.Millisecond) {
			return
		}
	}
}

func (handler *Handler) reconcileMessageBatches(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,CASE WHEN b.cancel_requested=1 THEN 'canceled' ELSE 'expired' END FROM message_batch_items i JOIN message_batches b ON b.id=i.batch_id WHERE i.state IN ('queued','claimed') AND (b.cancel_requested=1 OR b.created_at<=?)`, time.Now().Add(-messageBatchProcessingLifetime).UnixMilli())
	if err != nil {
		return err
	}
	type terminal struct {
		batchID, customID, state string
		ordinal                  int64
	}
	var items []terminal
	for rows.Next() {
		var item terminal
		if err := rows.Scan(&item.batchID, &item.ordinal, &item.customID, &item.state); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		line := messageBatchSimpleLine(item.customID, item.state)
		if _, err := handler.database.ExecContext(ctx, `UPDATE message_batch_items SET state=?,result_json=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('queued','claimed')`, item.state, line, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
			return err
		}
	}
	_, err = handler.database.ExecContext(ctx, `UPDATE message_batches SET processing_status='ended',ended_at=? WHERE processing_status<>'ended' AND NOT EXISTS (SELECT 1 FROM message_batch_items i WHERE i.batch_id=message_batches.id AND i.state IN ('queued','claimed','dispatching','settling'))`, time.Now().UnixMilli())
	return err
}

func cancelQueuedBatchItems(ctx context.Context, tx *sql.Tx, batchID, state string) error {
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,custom_id FROM message_batch_items WHERE batch_id=? AND state IN ('queued','claimed')`, batchID)
	if err != nil {
		return err
	}
	type item struct {
		ordinal  int64
		customID string
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.ordinal, &value.customID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE message_batch_items SET state=?,result_json=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('queued','claimed')`, state, messageBatchSimpleLine(item.customID, state), time.Now().UnixMilli(), batchID, item.ordinal); err != nil {
			return err
		}
	}
	return nil
}

func finishMessageBatchTx(ctx context.Context, tx *sql.Tx, batchID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE message_batches SET processing_status='ended',ended_at=? WHERE id=? AND processing_status<>'ended' AND NOT EXISTS (SELECT 1 FROM message_batch_items WHERE batch_id=? AND state IN ('queued','claimed','dispatching','settling'))`, time.Now().UnixMilli(), batchID, batchID)
	return err
}

func (handler *Handler) cancelBatchActive(batchID string) {
	handler.activeMu.Lock()
	var cancels []context.CancelFunc
	for id, cancel := range handler.active {
		if strings.HasPrefix(id, batchID+":") {
			cancels = append(cancels, cancel)
		}
	}
	handler.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func validAnthropicBatchMessage(raw []byte) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var kind string
	return json.Unmarshal(value["type"], &kind) == nil && kind == "message"
}

func messageBatchResultState(raw []byte) string {
	var line struct {
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &line) == nil && line.Result.Type == "succeeded" {
		return "succeeded"
	}
	return "errored"
}
func messageBatchSuccessLine(customID string, message json.RawMessage) []byte {
	return marshalBatchLine(customID, map[string]any{"type": "succeeded", "message": message})
}
func messageBatchSimpleLine(customID, state string) []byte {
	return marshalBatchLine(customID, map[string]any{"type": state})
}
func messageBatchErrorLine(customID, kind, message string) []byte {
	return marshalBatchLine(customID, map[string]any{"type": "errored", "error": map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}, "request_id": nil}})
}
func messageBatchErroredLine(customID string, raw []byte) []byte {
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil && value["type"] == "error" && value["error"] != nil {
		if _, exists := value["request_id"]; !exists {
			value["request_id"] = nil
		}
		return marshalBatchLine(customID, map[string]any{"type": "errored", "error": value})
	}
	return messageBatchErrorLine(customID, "api_error", "The provider request failed.")
}

func boundMessageBatchResult(customID, state string, line []byte) (string, []byte) {
	if len(line) <= maxInferenceBody {
		return state, line
	}
	return "errored", messageBatchErrorLine(customID, "api_error", "The provider response is too large to retain as a batch result.")
}
func marshalBatchLine(customID string, result any) []byte {
	value, _ := json.Marshal(map[string]any{"custom_id": customID, "result": result})
	return value
}
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
