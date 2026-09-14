package keys

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestKeyIsolationRotationAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	for _, id := range []string{"usr_a", "usr_b"} {
		if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id, email, display_name, password_hash, role, status, created_at, updated_at) VALUES (?, ?, ?, 'hash', 'member', 'active', ?, ?)", id, id+"@example.test", id, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE users SET inference_unrestricted = 1 WHERE id = 'usr_a'"); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB())
	if _, _, err := service.Create(ctx, "usr_b", Input{Label: "Escalation", Scopes: []string{"chat:generate"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("member grant escalation = %v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE users SET scopes_json = '["chat:generate"]', model_patterns_json = '["gpt-*"]', connection_ids_json = '["conn_main"]' WHERE id = 'usr_b'`); err != nil {
		t.Fatal(err)
	}
	narrow, _, err := service.Create(ctx, "usr_b", Input{Label: "Narrow pattern", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"gpt-5*"}, ConnectionIDs: []string{"conn_main"}})
	if err != nil {
		t.Fatalf("narrower wildcard grant = %v", err)
	}
	if _, _, err := service.Create(ctx, "usr_b", Input{Label: "Cross segment", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"gpt-/private*"}, ConnectionIDs: []string{"conn_main"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-segment wildcard grant = %v", err)
	}
	key, secret, err := service.Create(ctx, "usr_a", Input{Label: "Production", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"gpt-*"}, ConnectionIDs: []string{"conn_main"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, "usr_b", key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get = %v", err)
	}
	if items, _, err := service.List(ctx, "usr_b", 50, ""); err != nil || len(items) != 1 || items[0].ID != narrow.ID {
		t.Fatalf("cross-user list = %#v, %v", items, err)
	}
	if err := service.Revoke(ctx, "usr_b", key.ID, key.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user revoke = %v", err)
	}
	encoded, _ := json.Marshal(key)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("secret leaked from key resource")
	}
	principal, err := service.Authenticate(ctx, secret)
	if err != nil || principal.KeyID != key.ID || len(principal.Scopes) != 1 {
		t.Fatalf("authenticate = %#v, %v", principal, err)
	}
	if !principal.Allows("chat:generate", "gpt-5", "conn_main") || principal.Allows("responses:generate", "gpt-5", "conn_main") || principal.Allows("chat:generate", "gpt-5", "other") {
		t.Fatalf("unexpected effective grants: %#v", principal)
	}
	key, err = service.Update(ctx, "usr_a", key.ID, key.Revision, Input{Label: key.Label, State: key.State, Scopes: key.Scopes, ModelPatterns: key.ModelPatterns, ConnectionIDs: key.ConnectionIDs})
	if err != nil {
		t.Fatal(err)
	}
	selector := strings.SplitN(secret, "_", 3)[1]
	var secretExpiry any
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT expires_at FROM api_key_secrets WHERE selector = ?", selector).Scan(&secretExpiry); err != nil || secretExpiry != nil {
		t.Fatalf("secret expiry after key update = %#v, %v", secretExpiry, err)
	}
	rotated, newSecret, err := service.Rotate(ctx, "usr_a", key.ID, key.Revision)
	if err != nil || rotated.ID != key.ID || newSecret == secret {
		t.Fatalf("rotate = %#v, %v", rotated, err)
	}
	if _, err := service.Authenticate(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old secret = %v", err)
	}
	if _, err := service.Authenticate(ctx, newSecret); err != nil {
		t.Fatalf("new secret = %v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE api_keys SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute).UnixMilli(), key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Rotate(ctx, "usr_a", key.ID, rotated.Revision); !errors.Is(err, ErrExpired) {
		t.Fatalf("rotate expired key = %v", err)
	}
	if err := service.Revoke(ctx, "usr_a", key.ID, rotated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, newSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked secret = %v", err)
	}
	empty, _, err := service.Create(ctx, "usr_a", Input{Label: "No grants"})
	if err != nil || len(empty.Scopes) != 0 || len(empty.ModelPatterns) != 0 || len(empty.ConnectionIDs) != 0 {
		t.Fatalf("deny-by-default key = %#v, %v", empty, err)
	}
}
