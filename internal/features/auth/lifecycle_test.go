package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestLoginPasswordAndActivationLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	code, _, _ := service.PrepareSetup(ctx)
	owner, setupSession, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Login(ctx, LoginInput{Email: "missing@example.test", Password: "wrong-password-value"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("missing login error = %v", err)
	}
	_, loginSession, err := service.Login(ctx, LoginInput{Email: owner.Email, Password: "correct-horse-battery", UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.SessionInfo(ctx, loginSession)
	if err != nil {
		t.Fatal(err)
	}
	newSession, err := service.ChangePassword(ctx, current, "correct-horse-battery", "new-correct-horse-password", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{setupSession, loginSession} {
		if _, err := service.Session(ctx, old); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("old session remains valid: %v", err)
		}
	}
	if _, err := service.Session(ctx, newSession); err != nil {
		t.Fatalf("replacement session: %v", err)
	}

	activationCode := "member-activation-code"
	verifier := credentials.Verifier(activationCode)
	now := int64(1_900_000_000_000)
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id, email, display_name, password_hash, role, status, created_at, updated_at) VALUES ('usr_member', 'member@example.test', 'Member', '!', 'member', 'suspended', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO activation_tokens (verifier, user_id, purpose, expires_at, created_at) VALUES (?, 'usr_member', 'activation', ?, ?)", verifier[:], now, now); err != nil {
		t.Fatal(err)
	}
	member, memberSession, err := service.Activate(ctx, ActivateInput{Code: activationCode, Password: "member-secure-password"})
	if err != nil || member.Status != "active" {
		t.Fatalf("activate = %#v, %v", member, err)
	}
	if _, err := service.Session(ctx, memberSession); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Activate(ctx, ActivateInput{Code: activationCode, Password: "member-secure-password"}); !errors.Is(err, ErrInvalidActivation) {
		t.Fatalf("reused activation error = %v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE user_sessions SET last_seen_at = ? WHERE id = ?", time.Now().Add(-sessionIdleLifetime-time.Minute).UnixMilli(), memberSessionInfo(t, service, ctx, memberSession).ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(ctx, memberSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("idle session = %v", err)
	}
	var audits int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action IN ('account.activation', 'account.password_change')").Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("credential audits = %d, %v", audits, err)
	}
}

func TestStaleSessionCannotOverwriteCredentials(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	code, _, _ := service.PrepareSetup(ctx)
	owner, token, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	stale := memberSessionInfo(t, service, ctx, token)
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE users SET auth_revision = auth_revision + 1 WHERE id = ?", owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.UpdateProfile(ctx, stale, ProfileInput{Email: owner.Email, DisplayName: "Stale update"}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("stale profile update = %v", err)
	}
	_, token, err = service.Login(ctx, LoginInput{Email: owner.Email, Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	stale = memberSessionInfo(t, service, ctx, token)
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE users SET auth_revision = auth_revision + 1 WHERE id = ?", owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangePassword(ctx, stale, "correct-horse-battery", "new-correct-horse-password", "test"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("stale password change = %v", err)
	}
}

func TestLoginThrottleBoundsRotatingSubjects(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	for attempt := 0; attempt < 30; attempt++ {
		_, _, err := service.Login(ctx, LoginInput{Email: fmt.Sprintf("invalid-%d", attempt), Password: "bad", Source: fmt.Sprintf("203.0.113.1:%d", 4000+attempt)})
		if attempt >= 20 && !errors.Is(err, ErrTooManyAttempts) {
			t.Fatalf("attempt %d = %v", attempt, err)
		}
	}
	var rows int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM login_throttles").Scan(&rows); err != nil || rows > 22 {
		t.Fatalf("login throttle rows = %d, %v", rows, err)
	}
}

func memberSessionInfo(t *testing.T, service *Service, ctx context.Context, token string) AuthenticatedSession {
	t.Helper()
	current, err := service.SessionInfo(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return current
}
