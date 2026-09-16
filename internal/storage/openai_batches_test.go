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
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 28 {
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
	insertChatBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/chat/completions','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertChatBatch, "batch_chat"); err != nil {
		t.Fatal(err)
	}
	insertEmbeddingBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/embeddings','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertEmbeddingBatch, "batch_embedding"); err != nil {
		t.Fatal(err)
	}
	insertModerationBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/moderations','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertModerationBatch, "batch_moderation"); err != nil {
		t.Fatal(err)
	}
	insertImageBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/images/generations','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertImageBatch, "batch_image"); err != nil {
		t.Fatal(err)
	}
	insertImageEditBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/images/edits','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertImageEditBatch, "batch_image_edit"); err != nil {
		t.Fatal(err)
	}
	insertCompletionBatch := `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,'usr_batch','key_batch','file_input','/v1/completions','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`
	if _, err = database.ExecContext(ctx, insertCompletionBatch, "batch_completion"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_invalid','usr_batch','key_batch','file_input','/v1/unknown','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`); err == nil {
		t.Fatal("unsupported batch endpoint was accepted")
	}
	insertItem := `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_valid',1,'item','batch_req_1234567890123456',2,?,?,1024)`
	if _, err = database.ExecContext(ctx, insertItem, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_valid',2,'bad','batch_req_6543210987654321',2,?,?,1024)`, make([]byte, 17), make([]byte, 12)); err == nil {
		t.Fatal("invalid encrypted request length was accepted")
	}
	if _, err = database.ExecContext(ctx, `DELETE FROM openai_batches`); err != nil {
		t.Fatal(err)
	}
	var items int
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_batch_items`).Scan(&items); err != nil || items != 0 {
		t.Fatalf("cascaded batch items=%d error=%v", items, err)
	}
}

func TestOpenAIImageEditBatchMigrationPreservesV26Rows(t *testing.T) {
	ctx := context.Background()
	database, err := openDatabase(ctx, filepath.Join(t.TempDir(), "system.db"), "system", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	migrations, err := loadMigrations("system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 27 {
			break
		}
		if _, err = database.ExecContext(ctx, item.contents); err != nil {
			t.Fatalf("apply %s: %v", item.name, err)
		}
		if _, err = database.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 26 {
		t.Fatalf("pre-upgrade migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_upgrade','upgrade@example.test','Upgrade','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_upgrade','usr_upgrade','Upgrade','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_upgrade','usr_upgrade','key_upgrade','file_input','/v1/images/generations','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_upgrade',1,'preserved','batch_req_1234567890123456',2,?,?,1024)`, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	if err = applyMigrations(ctx, database, "system"); err != nil {
		t.Fatal(err)
	}
	var endpoint, customID string
	if err = database.QueryRowContext(ctx, `SELECT b.endpoint,i.custom_id FROM openai_batches b JOIN openai_batch_items i ON i.batch_id=b.id WHERE b.id='batch_upgrade'`).Scan(&endpoint, &customID); err != nil || endpoint != "/v1/images/generations" || customID != "preserved" {
		t.Fatalf("preserved endpoint=%q custom_id=%q error=%v", endpoint, customID, err)
	}
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left an invalid foreign key")
	}
}

func TestOpenAICompletionBatchMigrationPreservesV27Rows(t *testing.T) {
	ctx := context.Background()
	database, err := openDatabase(ctx, filepath.Join(t.TempDir(), "system.db"), "system", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	migrations, err := loadMigrations("system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 28 {
			break
		}
		if _, err = database.ExecContext(ctx, item.contents); err != nil {
			t.Fatalf("apply %s: %v", item.name, err)
		}
		if _, err = database.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 27 {
		t.Fatalf("pre-upgrade migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_completion_upgrade','completion-upgrade@example.test','Upgrade','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_completion_upgrade','usr_completion_upgrade','Upgrade','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_completion_upgrade','usr_completion_upgrade','key_completion_upgrade','file_input','/v1/images/edits','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_completion_upgrade',1,'preserved','batch_req_completion123456',2,?,?,1024)`, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	if err = applyMigrations(ctx, database, "system"); err != nil {
		t.Fatal(err)
	}
	var endpoint, customID string
	if err = database.QueryRowContext(ctx, `SELECT b.endpoint,i.custom_id FROM openai_batches b JOIN openai_batch_items i ON i.batch_id=b.id WHERE b.id='batch_completion_upgrade'`).Scan(&endpoint, &customID); err != nil || endpoint != "/v1/images/edits" || customID != "preserved" {
		t.Fatalf("preserved endpoint=%q custom_id=%q error=%v", endpoint, customID, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_completion_new','usr_completion_upgrade','key_completion_upgrade','file_input','/v1/completions','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_completion_unknown','usr_completion_upgrade','key_completion_upgrade','file_input','/v1/unknown','24h','model','in_progress','{}',3600,1,1,1,86400001,2592000001)`); err == nil {
		t.Fatal("unknown endpoint was accepted after migration")
	}
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left an invalid foreign key")
	}
}
