package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(authService)
	authHandler.Register(mux)
	NewHandler(New(store.SystemDB()), authHandler).Register(mux)

	claim := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(`{"email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`))
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

func TestRequestsHTTPFiltersByExactGatewayRequestID(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(authService)
	authHandler.Register(mux)
	NewHandler(New(store.SystemDB()), authHandler).Register(mux)

	claim := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(`{"email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`))
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set("Origin", "http://gateway.test")
	claimed := httptest.NewRecorder()
	mux.ServeHTTP(claimed, claim)
	if claimed.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", claimed.Code, claimed.Body.String())
	}
	var ownerID string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT id FROM users WHERE role='owner'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys (id,owner_user_id,label,state,scopes_json,model_patterns_json,connection_ids_json,created_at,updated_at) VALUES ('key_requests',?,'Requests','active','[]','[]','[]',?,?)`, ownerID, now, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"req_visible", "req_other"} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests (id,owner_user_id,key_id,operation,dialect,model_id,state,started_at) VALUES (?,?,'key_requests','chat','openai','model_test','succeeded',?)`, id, ownerID, now); err != nil {
			t.Fatal(err)
		}
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, authenticatedRequest(http.MethodGet, "http://gateway.test/api/v1/requests?request_id=req_visible", "", claimed.Result().Cookies(), false))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"req_visible"`) || strings.Contains(response.Body.String(), `"id":"req_other"`) {
		t.Fatalf("requests = %d: %s", response.Code, response.Body.String())
	}
	wrongState := httptest.NewRecorder()
	mux.ServeHTTP(wrongState, authenticatedRequest(http.MethodGet, "http://gateway.test/api/v1/requests?state=failed", "", claimed.Result().Cookies(), false))
	if wrongState.Code != http.StatusOK || !strings.Contains(wrongState.Body.String(), `"data":[]`) {
		t.Fatalf("state filter = %d: %s", wrongState.Code, wrongState.Body.String())
	}
	badRange := httptest.NewRecorder()
	mux.ServeHTTP(badRange, authenticatedRequest(http.MethodGet, "http://gateway.test/api/v1/requests?from=invalid", "", claimed.Result().Cookies(), false))
	if badRange.Code != http.StatusBadRequest {
		t.Fatalf("bad range = %d: %s", badRange.Code, badRange.Body.String())
	}
}

func TestBreakdownHTTPRequiresSessionAndValidDimension(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(auth.New(store.SystemDB()))
	authHandler.Register(mux)
	NewHandler(New(store.SystemDB()), authHandler).Register(mux)

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?dimension=key", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", unauthorized.Code)
	}
	claim := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(`{"email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`))
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set("Origin", "http://gateway.test")
	claimed := httptest.NewRecorder()
	mux.ServeHTTP(claimed, claim)
	if claimed.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", claimed.Code, claimed.Body.String())
	}
	valid := httptest.NewRecorder()
	mux.ServeHTTP(valid, authenticatedRequest(http.MethodGet, "http://gateway.test/api/v1/usage/breakdown?dimension=key", "", claimed.Result().Cookies(), false))
	if valid.Code != http.StatusOK || !strings.Contains(valid.Body.String(), `"data":[]`) {
		t.Fatalf("valid breakdown = %d: %s", valid.Code, valid.Body.String())
	}
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, authenticatedRequest(http.MethodGet, "http://gateway.test/api/v1/usage/breakdown?dimension=all&limit=1000", "", claimed.Result().Cookies(), false))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid breakdown = %d: %s", invalid.Code, invalid.Body.String())
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
