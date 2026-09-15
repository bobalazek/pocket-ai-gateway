package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWebSearchUsageMigrationsEnforceBounds(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedWebSearchSnapshotRows(t, ctx, store)

	var systemVersion, dataVersion, maxCalls, callCount, dailyCalls int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&systemVersion); err != nil || systemVersion < 17 {
		t.Fatalf("system migration version=%d error=%v", systemVersion, err)
	}
	if err := store.DataDB().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&dataVersion); err != nil || dataVersion < 4 {
		t.Fatalf("data migration version=%d error=%v", dataVersion, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT web_search_max_calls,web_search_call_count FROM attempts WHERE id='att_web'`).Scan(&maxCalls, &callCount); err != nil || maxCalls != 2 || callCount != 1 {
		t.Fatalf("attempt web search=%d/%d error=%v", maxCalls, callCount, err)
	}
	if err := store.DataDB().QueryRowContext(ctx, `SELECT web_search_calls FROM usage_daily WHERE owner_user_id='usr_web'`).Scan(&dailyCalls); err != nil || dailyCalls != 1 {
		t.Fatalf("daily web search=%d error=%v", dailyCalls, err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE attempts SET web_search_max_calls=5 WHERE id='att_web'`); err == nil {
		t.Fatal("system migration accepted an out-of-range maximum")
	}
	if _, err := store.DataDB().ExecContext(ctx, `UPDATE usage_daily SET web_search_calls=-1 WHERE owner_user_id='usr_web'`); err == nil {
		t.Fatal("data migration accepted a negative aggregate")
	}
}

func seedWebSearchSnapshotRows(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_web','web@example.test','Web','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_web','usr_web','Web','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests(id,owner_user_id,key_id,operation,dialect,model_id,state,started_at,finished_at) VALUES('req_web','usr_web','key_web','responses','responses','model','succeeded',1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO attempts(id,request_id,ordinal,connection_id,model_id,state,usage_status,web_search_max_calls,web_search_call_count,started_at,finished_at) VALUES('att_web','req_web',1,'conn','model','succeeded','provider_reported',2,1,1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DataDB().ExecContext(ctx, `INSERT INTO usage_daily(date,owner_user_id,key_id,model_id,connection_id,web_search_calls) VALUES('2026-01-01','usr_web','key_web','model','conn',1)`); err != nil {
		t.Fatal(err)
	}
}
