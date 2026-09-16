package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAIUploadsMigrationEnforcesLifecycleEncryptionAndCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := store.SystemDB()
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 22 {
		t.Fatalf("system migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_upload','upload@example.test','Upload','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_upload','usr_upload','Upload','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	insertUpload := `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,status,created_at,expires_at,completed_at,cancelled_at) VALUES(?,'usr_upload','key_upload',?,'batch','application/jsonl',?,?,?,1,3600001,?,?)`
	if _, err = database.ExecContext(ctx, insertUpload, "upload_valid", "batch.jsonl", 1, 3600, "pending", nil, nil); err != nil {
		t.Fatal(err)
	}
	insertPart := `INSERT INTO openai_upload_parts(id,upload_id,bytes,ciphertext,nonce,created_at) VALUES(?,'upload_valid',?,?,?,1)`
	if _, err = database.ExecContext(ctx, insertPart, "part_valid", 1, make([]byte, 17), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"purpose":             `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_purpose','usr_upload','key_upload','batch.jsonl','fine-tune','application/jsonl',1,3600,1,3600001)`,
		"expected bytes":      `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_size','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',16777217,3600,1,3600001)`,
		"file expiry":         `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_file_expiry','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',1,3599,1,3600001)`,
		"maximum file expiry": `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_max_file_expiry','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',1,2592001,1,3600001)`,
		"upload expiry":       `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_expiry','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',1,3600,1,3600002)`,
		"completed lifecycle": `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,status,created_at,expires_at) VALUES('upload_completed','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',1,3600,'completed',1,3600001)`,
		"cancelled lifecycle": `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,status,created_at,expires_at) VALUES('upload_cancelled','usr_upload','key_upload','batch.jsonl','batch','application/jsonl',1,3600,'cancelled',1,3600001)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, statement); err == nil {
				t.Fatal("invalid upload row was accepted")
			}
		})
	}
	for name, values := range map[string][]any{
		"size":       {"part_size", 16_777_217, make([]byte, 17), make([]byte, 12)},
		"ciphertext": {"part_cipher", 1, make([]byte, 16), make([]byte, 12)},
		"nonce":      {"part_nonce", 1, make([]byte, 17), make([]byte, 11)},
	} {
		t.Run("part "+name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, insertPart, values...); err == nil {
				t.Fatal("invalid upload part was accepted")
			}
		})
	}
	if _, err = database.ExecContext(ctx, `DELETE FROM openai_uploads WHERE id='upload_valid'`); err != nil {
		t.Fatal(err)
	}
	var parts int
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_upload_parts`).Scan(&parts); err != nil || parts != 0 {
		t.Fatalf("cascaded upload parts=%d error=%v", parts, err)
	}
}
