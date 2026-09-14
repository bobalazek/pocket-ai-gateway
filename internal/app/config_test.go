package app

import (
	"bytes"
	"context"
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
	setupCode, _, _ := service.PrepareSetup(ctx)
	_, session, err := service.Claim(ctx, auth.ClaimInput{SetupCode: setupCode, Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
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

func TestWriteSetupCodeCreatesProtectedFile(t *testing.T) {
	directory := t.TempDir()
	if err := writeSetupCode(directory, "test-code"); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "setup-code")
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "test-code\n" || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("setup file contents/permissions = %q %o", contents, info.Mode().Perm())
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
	cfg, err := ParseServe([]string{"--listen", "0.0.0.0:8080", "--public-url", "https://gateway.example.test"}, func(string) string { return "" }, io.Discard)
	if err != nil || cfg.PublicURL != "https://gateway.example.test" {
		t.Fatalf("public URL config = %#v, %v", cfg, err)
	}
}

func TestSetupURLUsesPublicOrigin(t *testing.T) {
	if got := setupURL("https://gateway.example.test"); got != "https://gateway.example.test/_/setup/" {
		t.Fatalf("setup URL = %q", got)
	}
}
