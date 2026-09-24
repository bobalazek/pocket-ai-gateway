package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestDemoSeedIsDisposableAndPopulatesUsage(t *testing.T) {
	operatorDir := t.TempDir()
	sentinel := filepath.Join(operatorDir, "operator-data")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POCKET_AI_GATEWAY_DATA_DIR", operatorDir)
	ctx := context.Background()
	demo, err := newDemo(ctx, "http://127.0.0.1:18084")
	if err != nil {
		t.Fatal(err)
	}
	dir := demo.store.DataDir()
	t.Cleanup(demo.close)
	if dir == operatorDir {
		t.Fatal("demo used operator data directory")
	}
	connections, err := providers.New(demo.store.SystemDB(), nil).ListConnections(ctx, demo.owner)
	if err != nil || len(connections) != 3 {
		t.Fatalf("connections: %d, %v", len(connections), err)
	}
	for _, connection := range connections {
		endpoint, err := url.Parse(connection.BaseURL)
		if err != nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || endpoint.Scheme != "http" {
			t.Fatalf("non-loopback demo provider: %s", connection.BaseURL)
		}
	}
	summary, err := usage.New(demo.store.SystemDB()).Summary(ctx, demo.owner, usage.UsageQuery{})
	if err != nil || summary.Requests != 124 || summary.Attempts != 124 || len(summary.Points) != 7 || summary.InputTokens == 0 || summary.CacheReadInputTokens == 0 || summary.KnownCostUSD == "0" || summary.UnknownAttempts != 0 {
		t.Fatalf("usage: %+v, %v", summary, err)
	}
	var projected int
	if err := demo.store.DataDB().QueryRowContext(ctx, "SELECT SUM(requests) FROM usage_daily").Scan(&projected); err != nil || projected != 124 {
		t.Fatalf("projected requests: %d, %v", projected, err)
	}
	var keysAcrossModels int
	if err := demo.store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT key_id FROM requests GROUP BY key_id HAVING COUNT(DISTINCT model_id) > 1)").Scan(&keysAcrossModels); err != nil || keysAcrossModels != 3 {
		t.Fatalf("demo key/model diversity: %d, %v", keysAcrossModels, err)
	}
	if _, _, err := auth.New(demo.store.SystemDB()).Login(ctx, auth.LoginInput{Email: demo.owner.Email, Password: demo.password, Source: "127.0.0.1"}); err != nil {
		t.Fatalf("demo login: %v", err)
	}
	demo.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temporary data survived cleanup: %v", err)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "untouched" {
		t.Fatalf("operator data changed: %q, %v", content, err)
	}
}
