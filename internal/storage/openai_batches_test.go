package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAIBatchesMigrationEnforcesEncryptedItemsAndCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := store.SystemDB()
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 20 {
		t.Fatalf("system migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_batch','batch@example.test','Batch','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_batch','usr_batch','Batch','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	insertBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/responses','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertBatch, "batch_valid"); err != nil {
		t.Fatal(err)
	}
	insertItem := `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_valid',1,'item','batch_req_1234567890123456',2,?,?,1024)`
	if _, err = database.ExecContext(ctx, insertItem, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_valid',2,'bad','batch_req_6543210987654321',2,?,?,1024)`, make([]byte, 17), make([]byte, 12)); err == nil {
		t.Fatal("invalid encrypted request length was accepted")
	}
	if _, err = database.ExecContext(ctx, `DELETE FROM openai_batches WHERE id='batch_valid'`); err != nil {
		t.Fatal(err)
	}
	var items int
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_batch_items`).Scan(&items); err != nil || items != 0 {
		t.Fatalf("cascaded batch items=%d error=%v", items, err)
	}
}
