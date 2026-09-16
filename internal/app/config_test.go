package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestParseServePrecedence(t *testing.T) {
	getenv := func(key string) string {
		values := map[string]string{
			"POCKET_AI_GATEWAY_LISTEN":   "127.0.0.1:9000",
			"POCKET_AI_GATEWAY_DATA_DIR": "from-env",
		}
		return values[key]
	}

	cfg, err := ParseServe([]string{"--listen", "127.0.0.1:9100", "--data-dir", "from-flag"}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9100" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	wantDir, _ := filepath.Abs("from-flag")
	if cfg.DataDir != wantDir {
		t.Fatalf("data dir = %q, want %q", cfg.DataDir, wantDir)
	}
}

func TestParseUpdateRequiresTrustAndDefaultsToDryRun(t *testing.T) {
	if _, err := parseUpdate("v1.0.0", nil, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("update accepted a missing trust key")
	}
	getenv := func(name string) string {
		if name == "POCKET_AI_GATEWAY_UPDATE_PUBLIC_KEY" {
			return base64.StdEncoding.EncodeToString(make([]byte, 32))
		}
		return ""
	}
	options, err := parseUpdate("v1.0.0", []string{"--manifest-url", "https://releases.example.test/manifest.json", "--data-dir", "update-data"}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.Abs("update-data")
	if options.Apply || options.CurrentVersion != "v1.0.0" || options.ManifestURL != "https://releases.example.test/manifest.json" || options.DataDir != wantDir {
		t.Fatalf("update options = %#v", options)
	}
}

func TestOwnerResetWritesProtectedCodeAndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := auth.New(store.SystemDB())
	_, session, err := service.Claim(ctx, auth.ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	var output bytes.Buffer
	if status := Execute(ctx, "test", []string{"owner-reset", "--data-dir", dataDir}, func(string) string { return "" }, &output, &output); status != 0 {
		t.Fatalf("owner-reset status = %d: %s", status, output.String())
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "recovery-code"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), strings.TrimSpace(string(contents))) {
		t.Fatal("recovery code leaked to output")
	}
	store, err = storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service = auth.New(store.SystemDB())
	if _, err := service.Session(ctx, session); err == nil {
		t.Fatal("old session remains valid")
	}
	if _, _, err := service.Activate(ctx, auth.ActivateInput{Code: strings.TrimSpace(string(contents)), Password: "new-owner-password-value"}); err != nil {
		t.Fatal(err)
	}
}

func TestParseServeDefaultsToLoopback(t *testing.T) {
	cfg, err := ParseServe(nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != defaultListen {
		t.Fatalf("listen = %q, want %q", cfg.Listen, defaultListen)
	}
}

func TestServeRejectsEncryptedFilesWithoutMasterKey(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_file','file@example.test','File','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_file','usr_file','File','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES('file_startup','usr_file','key_file','batch.jsonl','batch',0,?,?,0,3600000)`, make([]byte, 16), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_startup','usr_file','key_file','file_startup','/v1/responses','24h','model','{}',3600,1,0,0,86400000,2592000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_startup',1,'startup','batch_req_1234567890123456',2,?,?,1024)`, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_startup','usr_file','key_file','batch.jsonl','batch','application/jsonl',1,3600,0,3600000)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_upload_parts(id,upload_id,bytes,ciphertext,nonce,created_at) VALUES('part_startup','upload_startup',1,?,?,0)`, make([]byte, 17), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DELETE FROM openai_batches WHERE id='batch_startup'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DELETE FROM openai_files WHERE id='file_startup'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	err = serve(ctx, "test", Config{Listen: "127.0.0.1:0", DataDir: dataDir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "master.key is missing for stored encrypted data") {
		t.Fatalf("startup error = %v", err)
	}
	if _, err = os.Stat(filepath.Join(dataDir, "master.key")); !os.IsNotExist(err) {
		t.Fatalf("missing master key was replaced: %v", err)
	}
}

func TestServeRecoversInterruptedBackupJobs(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().Exec(`INSERT INTO backup_jobs(id,state,destination,archive_name,started_at) VALUES('bak_interrupted','running','local','interrupted.pagbak',1)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, "test", Config{Listen: address, DataDir: dataDir}, io.Discard) }()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, requestErr := client.Get("http://" + address + "/readyz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case serveErr := <-done:
			t.Fatalf("serve stopped before readiness: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("serve did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "system.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer database.Close()
	var state, message string
	var finished sql.NullInt64
	if err = database.QueryRow(`SELECT state,error,finished_at FROM backup_jobs WHERE id='bak_interrupted'`).Scan(&state, &message, &finished); err != nil || state != "failed" || message != "backup outcome unknown after process restart or restore" || !finished.Valid {
		cancel()
		t.Fatalf("backup was not recovered: %q/%q/%v, error = %v", state, message, finished, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestParseServeRequiresPublicURLForNetworkBind(t *testing.T) {
	if _, err := ParseServe([]string{"--listen", "0.0.0.0:8080"}, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("network bind without public URL succeeded")
	}
	if _, err := ParseServe([]string{"--listen", "0.0.0.0:8080", "--public-url", "http://gateway.example.test"}, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("network bind with an insecure public URL succeeded")
	}
	if _, err := ParseServe([]string{"--listen", "0.0.0.0:8080", "--public-url", "http://gateway.example.test", "--allow-insecure-http"}, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("insecure flag allowed a non-loopback public URL")
	}
	cfg, err := ParseServe([]string{"--listen", "0.0.0.0:8080", "--public-url", "https://gateway.example.test"}, func(string) string { return "" }, io.Discard)
	if err != nil || cfg.PublicURL != "https://gateway.example.test" {
		t.Fatalf("public URL config = %#v, %v", cfg, err)
	}
	local, err := ParseServe([]string{"--listen", "0.0.0.0:8080", "--public-url", "http://localhost:8080", "--allow-insecure-http"}, func(string) string { return "" }, io.Discard)
	if err != nil || !local.AllowInsecureHTTP {
		t.Fatalf("explicit local HTTP config = %#v, %v", local, err)
	}
}

func TestSetupURLUsesPublicOrigin(t *testing.T) {
	if got := setupURL("https://gateway.example.test"); got != "https://gateway.example.test/_/setup/" {
		t.Fatalf("setup URL = %q", got)
	}
}

func TestEncryptedBackupCommandsRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := storage.Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		if name == "POCKET_AI_GATEWAY_BACKUP_KEY" {
			return base64.StdEncoding.EncodeToString(key)
		}
		return ""
	}
	archive := filepath.Join(root, "backup.pagbak")
	var output bytes.Buffer
	if status := Execute(ctx, "test", []string{"backup", "--data-dir", source, "--output", archive}, getenv, &output, &output); status != 0 {
		t.Fatalf("backup status = %d: %s", status, output.String())
	}
	restored := filepath.Join(root, "restored")
	output.Reset()
	if status := Execute(ctx, "test", []string{"restore-backup", "--data-dir", restored, "--archive", archive}, getenv, &output, &output); status != 0 {
		t.Fatalf("restore status = %d: %s", status, output.String())
	}
	restoredStore, err := storage.Open(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	restoredStore.Close()
}
