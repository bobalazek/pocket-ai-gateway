package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMessageBatchMigrationEnforcesLifecycleAndOwnership(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := store.SystemDB()
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 18 {
		t.Fatalf("system migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_batch','batch@example.test','Batch','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_batch','usr_batch','Batch','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,created_at,expires_at) VALUES('msgbatch_test','usr_batch','key_batch',1,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_test',1,'item_1','{}',1024)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `UPDATE message_batches SET cancel_requested=1 WHERE id='msgbatch_test'`); err == nil {
		t.Fatal("migration accepted cancellation without cancel_initiated_at")
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_test',5,'item_5','{}',1024)`); err == nil {
		t.Fatal("migration accepted more than four items")
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_test',2,'item_1','{}',1024)`); err == nil {
		t.Fatal("migration accepted a duplicate custom_id")
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_test',2,'invalid item','{}',1024)`); err == nil {
		t.Fatal("migration accepted an invalid custom_id")
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_test',2,'item_2','{}',16777217)`); err == nil {
		t.Fatal("migration accepted an oversized result reservation")
	}
	if _, err = database.ExecContext(ctx, `UPDATE message_batch_items SET state='succeeded',finished_at=2 WHERE batch_id='msgbatch_test' AND ordinal=1`); err == nil {
		t.Fatal("migration accepted a terminal item without a result")
	}
	if _, err = database.ExecContext(ctx, `DELETE FROM api_keys WHERE id='key_batch'`); err != nil {
		t.Fatal(err)
	}
	var batches, items int64
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batch_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if batches != 0 || items != 0 {
		t.Fatalf("key deletion left batches=%d items=%d", batches, items)
	}
}

func seedMessageBatchSnapshotRows(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,processing_status,created_at,ended_at,expires_at) VALUES('msgbatch_snapshot','usr_web','key_web','ended',1,2,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,result_json,state,reserved_result_bytes,finished_at) VALUES('msgbatch_snapshot',1,'snapshot_item','{"model":"model"}','{"type":"succeeded"}','succeeded',1024,2)`); err != nil {
		t.Fatal(err)
	}
}
