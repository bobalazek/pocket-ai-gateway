package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestSetupHTTPFlow(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store.SystemDB())
	code, _, err := service.PrepareSetup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(service).Register(mux)

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/auth/setup/status", nil))
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), code) {
		t.Fatalf("unsafe setup status: %d %s", status.Code, status.Body.String())
	}

	body := `{"setup_code":"` + code + `","email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`
	claimRequest := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(body))
	claimRequest.Header.Set("Content-Type", "application/json")
	claimRequest.Header.Set("Origin", "http://gateway.test")
	claim := httptest.NewRecorder()
	mux.ServeHTTP(claim, claimRequest)
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim status = %d: %s", claim.Code, claim.Body.String())
	}
	cookies := claim.Result().Cookies()
	if len(cookies) != 2 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[1].HttpOnly || cookies[1].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %#v", cookies)
	}

	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	sessionRequest.AddCookie(cookies[0])
	session := httptest.NewRecorder()
	mux.ServeHTTP(session, sessionRequest)
	if session.Code != http.StatusOK || !strings.Contains(session.Body.String(), `"email":"owner@example.test"`) {
		t.Fatalf("session response = %d: %s", session.Code, session.Body.String())
	}
	var sessionBody struct {
		User    User        `json:"user"`
		Session SessionView `json:"session"`
	}
	if err := json.Unmarshal(session.Body.Bytes(), &sessionBody); err != nil || sessionBody.Session.ID == "" || sessionBody.Session.CreatedAt == "" || sessionBody.Session.LastSeenAt == "" || !sessionBody.User.Grants.Unrestricted {
		t.Fatalf("session contract = %#v, %v", sessionBody, err)
	}
	recoveryStatus := httptest.NewRecorder()
	mux.ServeHTTP(recoveryStatus, httptest.NewRequest(http.MethodGet, "/api/v1/auth/setup/status", nil))
	if recoveryStatus.Code != http.StatusOK || !strings.Contains(recoveryStatus.Body.String(), `"setup_recovery_available":true`) {
		t.Fatalf("recovery status = %d: %s", recoveryStatus.Code, recoveryStatus.Body.String())
	}

	deniedRequest := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(body))
	deniedRequest.Header.Set("Content-Type", "application/json")
	deniedRequest.Header.Set("Origin", "https://other.test")
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", denied.Code)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRequest.AddCookie(cookies[1])
	logoutRequest.Header.Set("Origin", "http://gateway.test")
	logoutDenied := httptest.NewRecorder()
	mux.ServeHTTP(logoutDenied, logoutRequest)
	if logoutDenied.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF = %d", logoutDenied.Code)
	}
	logoutRequest = httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRequest.AddCookie(cookies[1])
	logoutRequest.Header.Set("Origin", "http://gateway.test")
	logoutRequest.Header.Set("X-CSRF-Token", cookies[1].Value)
	logout := httptest.NewRecorder()
	mux.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d: %s", logout.Code, logout.Body.String())
	}
}

func TestDecodeJSONRejectsOversizedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"value":    `{"value":"` + strings.Repeat("a", 65<<10) + `"}`,
		"trailing": `{}` + strings.Repeat(" ", 65<<10),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			var input struct {
				Value string `json:"value"`
			}
			if DecodeJSON(response, request, &input) || response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized response = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCredentialMutationClearsStaleSession(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "https://gateway.test/api/v1/auth/password", nil)
	NewHandler(nil).writeCredentialError(response, request, ErrInvalidSession, "failed", "failed")
	if response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 2 {
		t.Fatalf("stale-session response = %d, cookies = %#v", response.Code, response.Result().Cookies())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge != -1 {
			t.Fatalf("cookie %s was not cleared", cookie.Name)
		}
	}
}

func TestRequireRevision(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/test", nil)
	request.Header.Set("If-Match", `"7"`)
	response := httptest.NewRecorder()
	revision, ok := RequireRevision(response, request)
	if !ok || revision != 7 || ETag(revision) != `"7"` {
		t.Fatalf("revision = %d, ok = %v, etag = %q", revision, ok, ETag(revision))
	}

	request.Header.Del("If-Match")
	response = httptest.NewRecorder()
	if _, ok := RequireRevision(response, request); ok || response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match response = %d: %s", response.Code, response.Body.String())
	}
}
