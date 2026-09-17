package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestAdapterScriptHTTPRequiresSessionCSRFAndRevision(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	providerService := New(store.SystemDB(), make([]byte, 32))
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(authService)
	authHandler.Register(mux)
	NewHandler(providerService, authHandler).Register(mux)

	claim := httptest.NewRequest(http.MethodPost, "http://gateway.test/api/v1/auth/setup/claim", strings.NewReader(`{"email":"owner@example.test","display_name":"Owner","password":"correct-horse-battery"}`))
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set("Origin", "http://gateway.test")
	claimed := httptest.NewRecorder()
	mux.ServeHTTP(claimed, claim)
	if claimed.Code != http.StatusCreated {
		t.Fatalf("claim=%d: %s", claimed.Code, claimed.Body.String())
	}
	var claimBody struct {
		User auth.User `json:"user"`
	}
	if err = json.Unmarshal(claimed.Body.Bytes(), &claimBody); err != nil {
		t.Fatal(err)
	}
	connection, err := providerService.CreateConnection(ctx, claimBody.User, ConnectionInput{Name: "Custom", Adapter: "openai_compatible", BaseURL: "https://provider.example/v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://gateway.test/api/v1/connections/" + connection.ID + "/adapter-script"
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, endpoint, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized GET=%d", unauthorized.Code)
	}

	cookies := claimed.Result().Cookies()
	get := httptest.NewRequest(http.MethodGet, endpoint, nil)
	for _, cookie := range cookies {
		get.AddCookie(cookie)
	}
	got := httptest.NewRecorder()
	mux.ServeHTTP(got, get)
	if got.Code != http.StatusOK || got.Header().Get("ETag") != `"1"` || !strings.Contains(got.Body.String(), `"request_script":""`) {
		t.Fatalf("GET=%d etag=%q body=%s", got.Code, got.Header().Get("ETag"), got.Body.String())
	}

	body := `{"request_script":"(input) => ({body: input.body})","response_script":""}`
	withoutCSRF := adapterScriptRequest(http.MethodPut, endpoint, body, cookies, "1", false)
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, withoutCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("without CSRF=%d", denied.Code)
	}
	missingRevision := httptest.NewRecorder()
	mux.ServeHTTP(missingRevision, adapterScriptRequest(http.MethodPut, endpoint, body, cookies, "", true))
	if missingRevision.Code != http.StatusPreconditionRequired {
		t.Fatalf("without revision=%d", missingRevision.Code)
	}
	updated := httptest.NewRecorder()
	mux.ServeHTTP(updated, adapterScriptRequest(http.MethodPut, endpoint, body, cookies, "1", true))
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("PUT=%d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
	stale := httptest.NewRecorder()
	mux.ServeHTTP(stale, adapterScriptRequest(http.MethodPut, endpoint, body, cookies, "1", true))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale PUT=%d body=%s", stale.Code, stale.Body.String())
	}
}

func adapterScriptRequest(method, target, body string, cookies []*http.Cookie, revision string, csrf bool) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://gateway.test")
	if revision != "" {
		request.Header.Set("If-Match", `"`+revision+`"`)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", cookies[1].Value)
	}
	return request
}
