package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if err != nil || summary.Requests != 124 || summary.Attempts != 124 || len(summary.Points) != 7 || summary.InputTokens == 0 || summary.CacheReadInputTokens == 0 || summary.KnownCostUSD == "0" || summary.UnknownAttempts != 5 {
		t.Fatalf("usage: %+v, %v", summary, err)
	}
	var succeeded, failed, recent, recentFailed, dialects, failedDialects, models, keyCount, connectionCount int
	if err := demo.store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE state='succeeded'),COUNT(*) FILTER (WHERE state='failed'),COUNT(*) FILTER (WHERE started_at>=?),COUNT(*) FILTER (WHERE state='failed' AND started_at>=?),COUNT(DISTINCT dialect),COUNT(DISTINCT dialect) FILTER (WHERE state='failed'),COUNT(DISTINCT model_id),COUNT(DISTINCT key_id) FROM requests`, time.Now().Add(-time.Hour).UnixMilli(), time.Now().Add(-time.Hour).UnixMilli()).Scan(&succeeded, &failed, &recent, &recentFailed, &dialects, &failedDialects, &models, &keyCount); err != nil {
		t.Fatal(err)
	}
	if err := demo.store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(DISTINCT connection_id) FROM attempts").Scan(&connectionCount); err != nil {
		t.Fatal(err)
	}
	if succeeded != 112 || failed != 12 || recent != 31 || recentFailed != 6 || dialects != 3 || failedDialects != 3 || models != 3 || keyCount != 3 || connectionCount != 3 {
		t.Fatalf("requests: succeeded=%d failed=%d recent=%d recent_failed=%d dialects=%d failed_dialects=%d models=%d keys=%d connections=%d", succeeded, failed, recent, recentFailed, dialects, failedDialects, models, keyCount, connectionCount)
	}
	var streaming, synchronous, timed, outsideRequest int
	if err := demo.store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE r.streaming=1),COUNT(*) FILTER (WHERE r.streaming=0),COUNT(a.first_byte_at),
		COUNT(*) FILTER (WHERE a.first_byte_at IS NOT NULL AND (a.first_byte_at < r.started_at OR a.first_byte_at > r.finished_at))
		FROM requests r JOIN attempts a ON a.request_id=r.id`).Scan(&streaming, &synchronous, &timed, &outsideRequest); err != nil {
		t.Fatal(err)
	}
	if streaming == 0 || synchronous == 0 || timed == 0 || outsideRequest != 0 {
		t.Fatalf("timing: streaming=%d synchronous=%d with_first_byte=%d first_byte_outside_request=%d", streaming, synchronous, timed, outsideRequest)
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
