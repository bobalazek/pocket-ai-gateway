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

	if required, err := service.SetupRequired(ctx); err != nil || !required {
		t.Fatalf("SetupRequired() = %v, %v", required, err)
	}
	user, session, err := service.Claim(ctx, ClaimInput{Email: "OWNER@example.test", DisplayName: " Owner ", Password: "correct-horse-battery"})
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
	var setupAudits int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action = 'owner.setup_claim'").Scan(&setupAudits); err != nil || setupAudits != 1 {
		t.Fatalf("setup audits = %d, %v", setupAudits, err)
	}
	if _, _, err := service.Claim(ctx, ClaimInput{Email: "second@example.test", DisplayName: "Second", Password: "correct-horse-battery"}); !errors.Is(err, ErrSetupComplete) {
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
	passwordSlots <- struct{}{}
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots; <-passwordSlots }()

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, claimErr := service.Claim(ctx, ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
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
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := range 2 {
		go func() {
			ready.Done()
			<-start
			_, _, claimErr := service.Claim(ctx, ClaimInput{Email: "owner" + string(rune('a'+index)) + "@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
			results <- claimErr
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrSetupComplete) {
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
