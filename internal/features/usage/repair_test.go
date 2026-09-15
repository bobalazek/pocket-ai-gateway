package usage

import (
	"testing"
	"time"
)

func TestRepairStaleRequestsWaitsForActiveMessageBatchItem(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "messages/batches", Scope: "chat:generate", Dialect: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "batch-repair-settlement", State: "succeeded", UsageStatus: "unknown"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleRequestRepairAge - time.Minute).UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE requests SET started_at=? WHERE id=?`, old, admission.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE attempts SET started_at=?,finished_at=? WHERE id=?`, old, old, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,created_at,expires_at) VALUES('msgbatch_repair',?,?,?,?)`, owner.ID, keyID, now, now+int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,result_json,state,request_id,attempt_id,reserved_result_bytes) VALUES('msgbatch_repair',1,'repair_item','{}','{}','settling',?,?,1024)`, admission.RequestID, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	if repaired, repairErr := service.RepairStaleRequests(ctx); repairErr != nil || repaired != 0 {
		t.Fatalf("active batch repair=%d error=%v", repaired, repairErr)
	}
	var state string
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, admission.RequestID).Scan(&state); err != nil || state != "in_progress" {
		t.Fatalf("active batch request state=%q error=%v", state, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE message_batch_items SET state='errored',finished_at=? WHERE batch_id='msgbatch_repair' AND ordinal=1`, now); err != nil {
		t.Fatal(err)
	}
	if repaired, repairErr := service.RepairStaleRequests(ctx); repairErr != nil || repaired != 1 {
		t.Fatalf("terminal batch repair=%d error=%v", repaired, repairErr)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, admission.RequestID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("terminal batch request state=%q error=%v", state, err)
	}
}

func TestRepairStaleRequestsWaitsForActiveOpenAIBatchItem(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["responses:generate"]' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", Scope: "responses:generate", Dialect: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "openai-batch-repair-settlement", State: "succeeded", UsageStatus: "unknown"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleRequestRepairAge - time.Minute).UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE requests SET started_at=? WHERE id=?`, old, admission.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE attempts SET started_at=?,finished_at=? WHERE id=?`, old, old, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_repair',?,?,'file_input','/v1/responses','24h','model_test','{}',3600,1,?,?,?,?)`, owner.ID, keyID, now, now, now+int64(24*time.Hour/time.Millisecond), now+int64(30*24*time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,state,result_bytes,result_ciphertext,result_nonce,request_id,attempt_id,reserved_result_bytes) VALUES('batch_repair',1,'repair','batch_req_1234567890123456',2,?,?,'settling',2,?,?,?, ?,1024)`, make([]byte, 18), make([]byte, 12), make([]byte, 18), make([]byte, 12), admission.RequestID, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	if repaired, repairErr := service.RepairStaleRequests(ctx); repairErr != nil || repaired != 0 {
		t.Fatalf("active OpenAI batch repair=%d error=%v", repaired, repairErr)
	}
	var state string
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, admission.RequestID).Scan(&state); err != nil || state != "in_progress" {
		t.Fatalf("active OpenAI batch request state=%q error=%v", state, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE openai_batch_items SET state='failed',finished_at=? WHERE batch_id='batch_repair' AND ordinal=1`, now); err != nil {
		t.Fatal(err)
	}
	if repaired, repairErr := service.RepairStaleRequests(ctx); repairErr != nil || repaired != 1 {
		t.Fatalf("terminal OpenAI batch repair=%d error=%v", repaired, repairErr)
	}
}
