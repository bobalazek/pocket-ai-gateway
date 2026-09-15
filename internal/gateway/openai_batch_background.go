package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	openAIBatchMaxItems         = 4
	openAIBatchProcessingWindow = 24 * time.Hour
	openAIBatchRetention        = 30 * 24 * time.Hour
)

type openAIBatchItemContextKey struct{}

type openAIBatchJob struct {
	batchID, ownerID, keyID, customID, resultID string
	ordinal, total                              int64
	createdAt                                   int64
	requestBytes                                int64
	requestCiphertext, requestNonce             []byte
}

func (handler *Handler) sealOpenAIBatchPayload(batchID, keyID, customID string, ordinal int64, kind string, plaintext []byte) ([]byte, []byte, error) {
	return credentials.Seal(handler.masterKey, plaintext, openAIBatchPayloadAAD(batchID, keyID, customID, ordinal, kind, int64(len(plaintext))))
}

func (handler *Handler) openOpenAIBatchPayload(batchID, keyID, customID string, ordinal int64, kind string, size int64, ciphertext, nonce []byte) ([]byte, error) {
	return credentials.Open(handler.masterKey, ciphertext, nonce, openAIBatchPayloadAAD(batchID, keyID, customID, ordinal, kind, size))
}

func openAIBatchPayloadAAD(batchID, keyID, customID string, ordinal int64, kind string, size int64) []byte {
	value, _ := json.Marshal([]any{batchID, keyID, ordinal, customID, kind, size})
	return value
}

func openAIBatchResultLineLimit(total int64) int {
	if total < 1 {
		total = 1
	}
	return (maxFileBytes - int(total-1)) / int(total)
}

func openAIBatchResultReservation(total int64) int64 {
	return int64(openAIBatchResultLineLimit(total) + 29)
}

func (handler *Handler) recoverOpenAIBatches(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,i.result_id,i.state,b.key_id,b.expires_at,i.result_bytes,i.result_ciphertext,i.result_nonce FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id WHERE i.state IN ('claimed','dispatching','settling')`)
	if err != nil {
		return err
	}
	type item struct {
		batchID, customID, resultID, state, keyID string
		ordinal, expiresAt, resultBytes           int64
		ciphertext, nonce                         []byte
	}
	var items []item
	for rows.Next() {
		var value item
		var resultBytes sql.NullInt64
		if err := rows.Scan(&value.batchID, &value.ordinal, &value.customID, &value.resultID, &value.state, &value.keyID, &value.expiresAt, &resultBytes, &value.ciphertext, &value.nonce); err != nil {
			rows.Close()
			return err
		}
		if resultBytes.Valid {
			value.resultBytes = resultBytes.Int64
		}
		items = append(items, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		if item.state == "claimed" {
			if _, err := handler.database.ExecContext(ctx, `UPDATE openai_batch_items SET state='queued',lease_epoch='',claimed_at=NULL WHERE batch_id=? AND ordinal=? AND state='claimed'`, item.batchID, item.ordinal); err != nil {
				return err
			}
			continue
		}
		if item.state == "settling" {
			line, openErr := handler.openOpenAIBatchPayload(item.batchID, item.keyID, item.customID, item.ordinal, "result", item.resultBytes, item.ciphertext, item.nonce)
			if openErr != nil {
				return openErr
			}
			state := openAIBatchResultState(line)
			if _, err := handler.database.ExecContext(ctx, `UPDATE openai_batch_items SET state=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state='settling'`, state, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
				return err
			}
			continue
		}
		state, code, message := "interrupted_unknown", "server_error", "The gateway restarted while the provider outcome was unknown."
		if item.expiresAt <= time.Now().UnixMilli() {
			state, code, message = "expired", "batch_expired", "The Batch expired before this request completed."
		}
		line := openAIBatchErrorLine(item.resultID, item.customID, code, message)
		ciphertext, nonce, sealErr := handler.sealOpenAIBatchPayload(item.batchID, item.keyID, item.customID, item.ordinal, "result", line)
		if sealErr != nil {
			return sealErr
		}
		if _, err := handler.database.ExecContext(ctx, `UPDATE openai_batch_items SET state=?,result_bytes=?,result_ciphertext=?,result_nonce=?,usage_known=0,finished_at=? WHERE batch_id=? AND ordinal=? AND state='dispatching'`, state, len(line), ciphertext, nonce, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
			return err
		}
	}
	return handler.reconcileOpenAIBatches(ctx)
}

func (handler *Handler) claimOpenAIBatch(ctx context.Context) (openAIBatchJob, bool) {
	if err := handler.reconcileOpenAIBatches(ctx); err != nil {
		return openAIBatchJob{}, false
	}
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return openAIBatchJob{}, false
	}
	defer tx.Rollback()
	var job openAIBatchJob
	err = tx.QueryRowContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,i.result_id,i.request_bytes,i.request_ciphertext,i.request_nonce,b.owner_user_id,b.key_id,b.request_total,b.created_at FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id WHERE i.state='queued' AND b.status='in_progress' AND b.cancel_requested=0 AND b.expires_at>? ORDER BY b.created_at,i.ordinal LIMIT 1`, time.Now().UnixMilli()).Scan(&job.batchID, &job.ordinal, &job.customID, &job.resultID, &job.requestBytes, &job.requestCiphertext, &job.requestNonce, &job.ownerID, &job.keyID, &job.total, &job.createdAt)
	if err != nil {
		return openAIBatchJob{}, false
	}
	result, err := tx.ExecContext(ctx, `UPDATE openai_batch_items SET state='claimed',lease_epoch=?,claimed_at=? WHERE batch_id=? AND ordinal=? AND state='queued'`, handler.epoch, time.Now().UnixMilli(), job.batchID, job.ordinal)
	if err != nil {
		return openAIBatchJob{}, false
	}
	changed, _ := result.RowsAffected()
	if changed != 1 || tx.Commit() != nil {
		return openAIBatchJob{}, false
	}
	return job, true
}

func (handler *Handler) runOpenAIBatch(ctx context.Context, job openAIBatchJob) {
	principal, err := handler.keys.Principal(ctx, job.keyID)
	if err != nil || principal.OwnerUserID != job.ownerID || !principalHasScope(principal.Scopes, "batches:manage") || !principalHasScope(principal.Scopes, "responses:generate") {
		handler.finishOpenAIBatchItem(ctx, job, "failed", openAIBatchErrorLine(job.resultID, job.customID, "permission_denied", "The creating API key or its grants are no longer active."), "", "")
		return
	}
	body, err := handler.openOpenAIBatchPayload(job.batchID, job.keyID, job.customID, job.ordinal, "request", job.requestBytes, job.requestCiphertext, job.requestNonce)
	if err != nil {
		handler.finishOpenAIBatchItem(ctx, job, "failed", openAIBatchErrorLine(job.resultID, job.customID, "server_error", "The stored Batch request could not be opened."), "", "")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.finishOpenAIBatchItem(ctx, job, "failed", openAIBatchErrorLine(job.resultID, job.customID, "invalid_request_error", "The stored Batch request is invalid."), "", "")
		return
	}
	if !handler.transitionBackground(ctx, `UPDATE openai_batch_items SET state='dispatching',dispatch_started_at=? WHERE batch_id=? AND ordinal=? AND state='claimed' AND lease_epoch=?`, time.Now().UnixMilli(), job.batchID, job.ordinal, handler.epoch) {
		return
	}
	deadline := time.UnixMilli(job.createdAt).Add(openAIBatchProcessingWindow)
	jobContext, cancel := context.WithDeadline(ctx, deadline)
	handler.activeMu.Lock()
	handler.active[job.batchID+":"+fmt.Sprint(job.ordinal)] = cancel
	handler.activeMu.Unlock()
	defer func() {
		cancel()
		handler.activeMu.Lock()
		delete(handler.active, job.batchID+":"+fmt.Sprint(job.ordinal))
		handler.activeMu.Unlock()
	}()
	var canceled bool
	cancelReadErr := handler.database.QueryRowContext(ctx, `SELECT cancel_requested FROM openai_batches WHERE id=?`, job.batchID).Scan(&canceled)
	if ctx.Err() != nil {
		return
	}
	if canceled {
		handler.finishOpenAIBatchItem(ctx, job, "canceled", openAIBatchErrorLine(job.resultID, job.customID, "batch_cancelled", "The Batch was cancelled before this request executed."), "", "")
		return
	}
	if !time.Now().Before(deadline) {
		handler.finishOpenAIBatchItem(ctx, job, "expired", openAIBatchErrorLine(job.resultID, job.customID, "batch_expired", "The Batch expired before this request executed."), "", "")
		return
	}
	if cancelReadErr != nil {
		handler.finishOpenAIBatchItem(ctx, job, "failed", openAIBatchErrorLine(job.resultID, job.customID, "server_error", "The Batch cancellation state could not be checked."), "", "")
		return
	}
	envelope["background"], envelope["store"], envelope["stream"] = json.RawMessage(`false`), json.RawMessage(`false`), json.RawMessage(`false`)
	body, _ = json.Marshal(envelope)
	recorder := &memoryResponse{header: make(http.Header)}
	request, _ := http.NewRequestWithContext(context.WithValue(jobContext, openAIBatchItemContextKey{}, job.total), http.MethodPost, "/api/openai/v1/responses", bytes.NewReader(body))
	handler.forwardAuthorized(recorder, request, "responses", "responses:generate", "responses", "", nil, principal, body)
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
	_ = handler.database.QueryRowContext(ctx, `SELECT cancel_requested FROM openai_batches WHERE id=?`, job.batchID).Scan(&canceled)
	if !completedOutcome && canceled {
		handler.finishOpenAIBatchItem(ctx, job, "canceled", openAIBatchErrorLine(job.resultID, job.customID, "batch_cancelled", "The Batch was cancelled before this request completed."), recorder.requestID, recorder.attemptID)
		return
	}
	if !completedOutcome && !time.Now().Before(deadline) {
		handler.finishOpenAIBatchItem(ctx, job, "expired", openAIBatchErrorLine(job.resultID, job.customID, "batch_expired", "The Batch expired before this request completed."), recorder.requestID, recorder.attemptID)
		return
	}
	line, state := openAIBatchErrorLine(job.resultID, job.customID, "server_error", "The request could not be completed."), "failed"
	if completedOutcome && recorder.status >= 200 && recorder.status < 300 && json.Valid(recorder.body.Bytes()) {
		var publicModel string
		_ = json.Unmarshal(envelope["model"], &publicModel)
		if publicBody, rewriteErr := rewriteResponseModel(recorder.body.Bytes(), publicModel); rewriteErr == nil {
			line, state = openAIBatchSuccessLine(job.resultID, job.customID, recorder.status, recorder.requestID, publicBody), "succeeded"
		}
	} else if len(recorder.body.Bytes()) > 0 {
		line = openAIBatchProviderErrorLine(job.resultID, job.customID, recorder.body.Bytes())
	}
	line, state = boundOpenAIBatchResult(job, line, state)
	if recorder.requestID != "" {
		ciphertext, nonce, sealErr := handler.sealOpenAIBatchPayload(job.batchID, job.keyID, job.customID, job.ordinal, "result", line)
		if sealErr != nil || !handler.transitionBackground(ctx, `UPDATE openai_batch_items SET state='settling',result_bytes=?,result_ciphertext=?,result_nonce=?,request_id=?,attempt_id=? WHERE batch_id=? AND ordinal=? AND state='dispatching' AND lease_epoch=?`, len(line), ciphertext, nonce, recorder.requestID, nullIfEmpty(recorder.attemptID), job.batchID, job.ordinal, handler.epoch) {
			return
		}
	}
	handler.finishOpenAIBatchItem(ctx, job, state, line, recorder.requestID, recorder.attemptID)
}

func (handler *Handler) finishOpenAIBatchItem(ctx context.Context, job openAIBatchJob, state string, line []byte, requestID, attemptID string) {
	line, state = boundOpenAIBatchResult(job, line, state)
	ciphertext, nonce, err := handler.sealOpenAIBatchPayload(job.batchID, job.keyID, job.customID, job.ordinal, "result", line)
	if err != nil {
		return
	}
	for ctx.Err() == nil {
		result, updateErr := handler.database.ExecContext(ctx, `UPDATE openai_batch_items SET state=?,result_bytes=?,result_ciphertext=?,result_nonce=?,request_id=COALESCE(NULLIF(?,''),request_id),attempt_id=COALESCE(NULLIF(?,''),attempt_id),finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('claimed','dispatching','settling')`, state, len(line), ciphertext, nonce, requestID, attemptID, time.Now().UnixMilli(), job.batchID, job.ordinal)
		if updateErr == nil {
			changed, rowsErr := result.RowsAffected()
			if rowsErr == nil && changed == 1 {
				_ = handler.finalizeOpenAIBatch(ctx, job.batchID)
				return
			}
			if rowsErr == nil {
				return
			}
		}
		if !waitBackground(ctx, 250*time.Millisecond) {
			return
		}
	}
}

func (handler *Handler) reconcileOpenAIBatches(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT i.batch_id,i.ordinal,i.custom_id,i.result_id,b.key_id,CASE WHEN b.cancel_requested=1 THEN 'batch_cancelled' ELSE 'batch_expired' END FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id WHERE i.state IN ('queued','claimed') AND (b.cancel_requested=1 OR b.expires_at<=?)`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	type item struct {
		batchID, customID, resultID, keyID, reason string
		ordinal                                    int64
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.batchID, &value.ordinal, &value.customID, &value.resultID, &value.keyID, &value.reason); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		state, message := "expired", "The Batch expired before this request executed."
		if item.reason == "batch_cancelled" {
			state, message = "canceled", "The Batch was cancelled before this request executed."
		}
		line := openAIBatchErrorLine(item.resultID, item.customID, item.reason, message)
		ciphertext, nonce, err := handler.sealOpenAIBatchPayload(item.batchID, item.keyID, item.customID, item.ordinal, "result", line)
		if err != nil {
			return err
		}
		if _, err := handler.database.ExecContext(ctx, `UPDATE openai_batch_items SET state=?,result_bytes=?,result_ciphertext=?,result_nonce=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('queued','claimed')`, state, len(line), ciphertext, nonce, time.Now().UnixMilli(), item.batchID, item.ordinal); err != nil {
			return err
		}
		if err := handler.finalizeOpenAIBatch(ctx, item.batchID); err != nil {
			return err
		}
	}
	batchRows, err := handler.database.QueryContext(ctx, `SELECT id FROM openai_batches WHERE status IN ('in_progress','finalizing','cancelling')`)
	if err != nil {
		return err
	}
	var batchIDs []string
	for batchRows.Next() {
		var batchID string
		if err := batchRows.Scan(&batchID); err != nil {
			batchRows.Close()
			return err
		}
		batchIDs = append(batchIDs, batchID)
	}
	if err := errors.Join(batchRows.Err(), batchRows.Close()); err != nil {
		return err
	}
	for _, batchID := range batchIDs {
		if err := handler.finalizeOpenAIBatch(ctx, batchID); err != nil {
			return err
		}
	}
	return nil
}

func (handler *Handler) cancelOpenAIBatchQueuedTx(ctx context.Context, tx *sql.Tx, batchID, reason string) error {
	rows, err := tx.QueryContext(ctx, `SELECT i.ordinal,i.custom_id,i.result_id,b.key_id FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id WHERE i.batch_id=? AND i.state IN ('queued','claimed')`, batchID)
	if err != nil {
		return err
	}
	type item struct {
		customID, resultID, keyID string
		ordinal                   int64
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.ordinal, &value.customID, &value.resultID, &value.keyID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	state, message := "expired", "The Batch expired before this request executed."
	if reason == "batch_cancelled" {
		state, message = "canceled", "The Batch was cancelled before this request executed."
	}
	for _, item := range items {
		line := openAIBatchErrorLine(item.resultID, item.customID, reason, message)
		ciphertext, nonce, err := handler.sealOpenAIBatchPayload(batchID, item.keyID, item.customID, item.ordinal, "result", line)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE openai_batch_items SET state=?,result_bytes=?,result_ciphertext=?,result_nonce=?,finished_at=? WHERE batch_id=? AND ordinal=? AND state IN ('queued','claimed')`, state, len(line), ciphertext, nonce, time.Now().UnixMilli(), batchID, item.ordinal); err != nil {
			return err
		}
	}
	return nil
}

func (handler *Handler) cancelOpenAIBatchActive(batchID string) { handler.cancelBatchActive(batchID) }

func (handler *Handler) finalizeOpenAIBatch(ctx context.Context, batchID string) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerID, keyID, status string
	var cancelRequested bool
	var expirySeconds, total int64
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id,key_id,status,cancel_requested,output_expiry_seconds,request_total FROM openai_batches WHERE id=?`, batchID).Scan(&ownerID, &keyID, &status, &cancelRequested, &expirySeconds, &total)
	if err != nil || status == "completed" || status == "cancelled" || status == "expired" {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,custom_id,state,result_bytes,result_ciphertext,result_nonce,request_id,usage_known FROM openai_batch_items WHERE batch_id=? ORDER BY ordinal`, batchID)
	if err != nil {
		return err
	}
	var output, failures bytes.Buffer
	var completed, failed, seen int64
	var expiredItems bool
	usage := openAIBatchUsage{known: true}
	for rows.Next() {
		var ordinal int64
		var size sql.NullInt64
		var customID, state string
		var requestID sql.NullString
		var itemUsageKnown bool
		var ciphertext, nonce []byte
		if err := rows.Scan(&ordinal, &customID, &state, &size, &ciphertext, &nonce, &requestID, &itemUsageKnown); err != nil {
			rows.Close()
			return err
		}
		if state == "queued" || state == "claimed" || state == "dispatching" || state == "settling" {
			rows.Close()
			return nil
		}
		line, err := handler.openOpenAIBatchPayload(batchID, keyID, customID, ordinal, "result", size.Int64, ciphertext, nonce)
		if err != nil {
			rows.Close()
			return err
		}
		seen++
		usage.known = usage.known && itemUsageKnown
		if state == "succeeded" {
			completed++
			appendJSONL(&output, line)
			usage.known = usage.add(line) && usage.known
		} else {
			expiredItems = expiredItems || state == "expired"
			if state == "failed" || state == "interrupted_unknown" || requestID.Valid {
				usage.known = false
			}
			if state == "failed" || state == "interrupted_unknown" {
				failed++
			}
			appendJSONL(&failures, line)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if seen != total || output.Len() > maxFileBytes || failures.Len() > maxFileBytes {
		return errors.New("OpenAI Batch result set is invalid")
	}
	now := time.Now()
	fileExpiry := now.Add(time.Duration(expirySeconds) * time.Second)
	var outputID, errorID any
	if output.Len() > 0 {
		file, err := handler.insertGeneratedOpenAIFile(ctx, tx, ownerID, keyID, batchID+"_output.jsonl", output.Bytes(), fileExpiry)
		if err != nil {
			return err
		}
		outputID = file.ID
	}
	if failures.Len() > 0 {
		file, err := handler.insertGeneratedOpenAIFile(ctx, tx, ownerID, keyID, batchID+"_errors.jsonl", failures.Bytes(), fileExpiry)
		if err != nil {
			return err
		}
		errorID = file.ID
	}
	terminalStatus := "completed"
	if cancelRequested {
		terminalStatus = "cancelled"
	} else if expiredItems {
		terminalStatus = "expired"
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_batches SET status=?,output_file_id=?,error_file_id=?,request_completed=?,request_failed=?,usage_known=?,usage_input_tokens=?,usage_output_tokens=?,usage_cached_tokens=?,usage_reasoning_tokens=?,terminal_at=? WHERE id=? AND status IN ('in_progress','finalizing','cancelling')`, terminalStatus, outputID, errorID, completed, failed, usage.known, usage.input, usage.output, usage.cached, usage.reasoning, now.UnixMilli(), batchID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM openai_batch_items WHERE batch_id=?`, batchID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func appendJSONL(buffer *bytes.Buffer, line []byte) {
	if buffer.Len() > 0 {
		buffer.WriteByte('\n')
	}
	buffer.Write(line)
}

func openAIBatchSuccessLine(id, customID string, status int, requestID string, body []byte) []byte {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return openAIBatchErrorLine(id, customID, "server_error", "The provider returned invalid JSON.")
	}
	line, _ := json.Marshal(map[string]any{"id": id, "custom_id": customID, "response": map[string]any{"status_code": status, "request_id": requestID, "body": value}, "error": nil})
	return line
}

func openAIBatchProviderErrorLine(id, customID string, body []byte) []byte {
	code, message := "request_failed", "The request failed."
	var value struct {
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &value) == nil {
		if typed, ok := value.Error.Code.(string); ok && typed != "" {
			code = typed
		} else if value.Error.Type != "" {
			code = value.Error.Type
		}
		if value.Error.Message != "" {
			message = value.Error.Message
		}
	}
	return openAIBatchErrorLine(id, customID, code, message)
}

func openAIBatchErrorLine(id, customID, code, message string) []byte {
	line, _ := json.Marshal(map[string]any{"id": id, "custom_id": customID, "response": nil, "error": map[string]string{"code": code, "message": message}})
	return line
}

func openAIBatchResultState(line []byte) string {
	var value struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &value) == nil && (len(value.Error) == 0 || bytes.Equal(bytes.TrimSpace(value.Error), []byte("null"))) {
		return "succeeded"
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(line, &failure)
	switch failure.Error.Code {
	case "batch_cancelled":
		return "canceled"
	case "batch_expired":
		return "expired"
	default:
		return "failed"
	}
}

func boundOpenAIBatchResult(job openAIBatchJob, line []byte, state string) ([]byte, string) {
	if len(line) <= openAIBatchResultLineLimit(job.total) {
		return line, state
	}
	return openAIBatchErrorLine(job.resultID, job.customID, "response_too_large", "The response is too large to retain in the Batch result File."), "failed"
}

type openAIBatchUsage struct {
	known                            bool
	input, output, cached, reasoning int64
}

const maxOpenAIBatchUsage = int64(9_007_199_254_740_991)

func (usage *openAIBatchUsage) add(line []byte) bool {
	var value struct {
		Response struct {
			Body struct {
				Usage struct {
					Input        *int64 `json:"input_tokens"`
					Output       *int64 `json:"output_tokens"`
					Total        *int64 `json:"total_tokens"`
					InputDetails struct {
						Cached int64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
					OutputDetails struct {
						Reasoning int64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
			} `json:"body"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &value) != nil || value.Response.Body.Usage.Input == nil || value.Response.Body.Usage.Output == nil {
		return false
	}
	input, output := *value.Response.Body.Usage.Input, *value.Response.Body.Usage.Output
	cached, reasoning := value.Response.Body.Usage.InputDetails.Cached, value.Response.Body.Usage.OutputDetails.Reasoning
	if input < 0 || output < 0 || cached < 0 || cached > input || reasoning < 0 || reasoning > output || input > maxOpenAIBatchUsage-output || value.Response.Body.Usage.Total != nil && *value.Response.Body.Usage.Total != input+output || input > maxOpenAIBatchUsage-usage.input || output > maxOpenAIBatchUsage-usage.output || usage.input+input > maxOpenAIBatchUsage-(usage.output+output) || cached > maxOpenAIBatchUsage-usage.cached || reasoning > maxOpenAIBatchUsage-usage.reasoning {
		return false
	}
	usage.input += input
	usage.output += output
	usage.cached += cached
	usage.reasoning += reasoning
	return true
}
