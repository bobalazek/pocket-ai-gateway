package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAIFilesMigrationEnforcesEncryptionAndRetentionBounds(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := store.SystemDB()
	var version int64
	if err = database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 19 {
		t.Fatalf("system migration version=%d error=%v", version, err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_file','file@example.test','File','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_file','usr_file','File','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`
	if _, err = database.ExecContext(ctx, insert, "file_valid", "usr_file", "key_file", "batch.jsonl", "batch", 1, make([]byte, 17), make([]byte, 12), 1, 3_600_001); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]any{
		"purpose":    {"file_purpose", "usr_file", "key_file", "batch.jsonl", "fine-tune", 1, make([]byte, 17), make([]byte, 12), 1, 3_600_001},
		"size":       {"file_size", "usr_file", "key_file", "batch.jsonl", "batch", 16_777_217, make([]byte, 17), make([]byte, 12), 1, 3_600_001},
		"ciphertext": {"file_cipher", "usr_file", "key_file", "batch.jsonl", "batch", 1, make([]byte, 16), make([]byte, 12), 1, 3_600_001},
		"nonce":      {"file_nonce", "usr_file", "key_file", "batch.jsonl", "batch", 1, make([]byte, 17), make([]byte, 11), 1, 3_600_001},
		"expiry":     {"file_expiry", "usr_file", "key_file", "batch.jsonl", "batch", 1, make([]byte, 17), make([]byte, 12), 1, 3_600_000},
		"filename":   {"file_name", "usr_file", "key_file", "bad\x00name", "batch", 1, make([]byte, 17), make([]byte, 12), 1, 3_600_001},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, insert, values...); err == nil {
				t.Fatal("invalid file row was accepted")
			}
		})
	}
}
