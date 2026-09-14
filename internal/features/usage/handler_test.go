package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestWriteErrorTreatsRangeFailuresAsBadRequests(t *testing.T) {
	response := httptest.NewRecorder()
	(&Handler{}).writeError(response, errors.New("settlement token count is too large"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestPolicyHTTPFlowRequiresSessionCSRFAndRevision(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	code, _, err := authService.PrepareSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(authService)
	authHandler.Register(mux)
	NewHandler(New(store.SystemDB()), authHandler).Register(mux)

	claim := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(`{"setup_code":"`+code+`","email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`))
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set("Origin", "http://gateway.test")
	claimed := httptest.NewRecorder()
	mux.ServeHTTP(claimed, claim)
	if claimed.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", claimed.Code, claimed.Body.String())
	}
	cookies := claimed.Result().Cookies()

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/admin/policies", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list = %d", unauthorized.Code)
	}

	body := `{"scope_kind":"instance","scope_id":"","metric":"requests","algorithm":"quota","period":"day","window_seconds":0,"limit_units":100,"limit_usd":"","refill_units":0,"refill_interval_ms":0}`
	withoutCSRF := authenticatedRequest(http.MethodPost, "http://gateway.test/api/v1/admin/policies", body, cookies, false)
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, withoutCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d", denied.Code)
	}

	created := httptest.NewRecorder()
	mux.ServeHTTP(created, authenticatedRequest(http.MethodPost, "http://gateway.test/api/v1/admin/policies", body, cookies, true))
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` || !strings.Contains(created.Body.String(), `"metric":"requests"`) {
		t.Fatalf("create = %d %s: %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}

	missingRevision := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodPatch, "http://gateway.test/api/v1/admin/policies/pol_missing", `{"limit_units":50,"limit_usd":"","enabled":true}`, cookies, true)
	mux.ServeHTTP(missingRevision, request)
	if missingRevision.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision = %d", missingRevision.Code)
	}
}

func authenticatedRequest(method, target, body string, cookies []*http.Cookie, csrf bool) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://gateway.test")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", cookies[1].Value)
	}
	return request
}
