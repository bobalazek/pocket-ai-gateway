package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
