package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestOwnerSetupIsOneTimeAndCreatesSession(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())

	code, required, err := service.PrepareSetup(ctx)
	if err != nil || !required || code == "" {
		t.Fatalf("PrepareSetup() = %q, %v, %v", code, required, err)
	}
	if _, _, err := service.Claim(ctx, ClaimInput{SetupCode: "wrong", Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"}); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("invalid code error = %v", err)
	}

	user, session, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "OWNER@example.test", DisplayName: " Owner ", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "owner@example.test" || user.DisplayName != "Owner" || session == "" {
		t.Fatalf("unexpected user/session: %#v %q", user, session)
	}
	if current, err := service.Session(ctx, session); err != nil || current.ID != user.ID {
		t.Fatalf("Session() = %#v, %v", current, err)
	}
	if required, err := service.SetupRequired(ctx); err != nil || required {
		t.Fatalf("SetupRequired() = %v, %v", required, err)
	}
	if state, err := service.SetupStatus(ctx); err != nil || !state.Recoverable {
		t.Fatalf("SetupStatus() = %#v, %v", state, err)
	}
	if nextCode, required, err := service.PrepareSetup(ctx); err != nil || required || nextCode != "" {
		t.Fatalf("PrepareSetup() after claim = %q, %v, %v", nextCode, required, err)
	}
	current, retrySession, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil || current.ID != user.ID || retrySession == "" {
		t.Fatalf("retry claim = %#v, %q, %v", current, retrySession, err)
	}
	if _, err := service.Session(ctx, session); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("replaced setup session error = %v", err)
	}
	if retryUser, err := service.Session(ctx, retrySession); err != nil || retryUser.ID != user.ID {
		t.Fatalf("retry session = %#v, %v", retryUser, err)
	}
	var setupAudits int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action IN ('owner.setup_claim', 'owner.setup_replay')").Scan(&setupAudits); err != nil || setupAudits != 2 {
		t.Fatalf("setup audits = %d, %v", setupAudits, err)
	}
	if _, _, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner@example.test", DisplayName: "Owner", Password: "wrong-password-value"}); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("retry with wrong password error = %v", err)
	}
	if _, _, err := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "second@example.test", DisplayName: "Second", Password: "correct-horse-battery"}); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("second claim error = %v", err)
	}

	var passwordHash string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", user.ID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(passwordHash, "$argon2id$") || strings.Contains(passwordHash, "correct-horse-battery") {
		t.Fatalf("password was not safely hashed: %q", passwordHash)
	}
}

func TestConcurrentOwnerClaimsRespectPasswordSlots(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	code, _, err := service.PrepareSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	passwordSlots <- struct{}{}
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots; <-passwordSlots }()

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, claimErr := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
			results <- claimErr
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; !errors.Is(err, ErrTooManyAttempts) {
			t.Fatalf("claim outside password slots = %v", err)
		}
	}
}

func TestOnlyOneConcurrentOwnerClaimSucceeds(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	code, _, err := service.PrepareSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := range 2 {
		go func() {
			ready.Done()
			<-start
			_, _, claimErr := service.Claim(ctx, ClaimInput{SetupCode: code, Email: "owner" + string(rune('a'+index)) + "@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
			results <- claimErr
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrSetupComplete) && !errors.Is(err, ErrInvalidSetupCode) {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful claims = %d, want 1", successes)
	}
}

func TestPasswordMinimumCountsCharacters(t *testing.T) {
	_, _, err := validateClaim(ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "😀😀😀"})
	var inputError *InputError
	if !errors.As(err, &inputError) {
		t.Fatalf("short Unicode password error = %v", err)
	}
}
