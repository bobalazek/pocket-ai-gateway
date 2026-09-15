package operations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestOwnerOnlyOperationsAndRecentAuthentication(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	owner, ownerToken, err := authService.Claim(ctx, auth.ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	adminToken := "synthetic-admin-session-token"
	adminVerifier := credentials.Verifier(adminToken)
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,created_at,updated_at) VALUES('usr_admin','admin@example.test','Admin','hash','admin','active',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO user_sessions(id,verifier,user_id,expires_at,created_at,last_seen_at,authenticated_at,auth_revision,user_agent) VALUES('ses_admin',?,'usr_admin',?,?,?,?,1,'test')`, adminVerifier[:], now+time.Hour.Milliseconds(), now, now, now); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" }), auth.NewHandler(authService)).Register(mux)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: adminToken})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("admin settings status = %d", response.Code)
	}

	ownerVerifier := credentials.Verifier(ownerToken)
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE user_sessions SET authenticated_at=0 WHERE verifier=?`, ownerVerifier[:]); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/config/export", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: ownerToken})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("stale owner export status = %d for %s", response.Code, owner.ID)
	}
}
