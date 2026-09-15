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
