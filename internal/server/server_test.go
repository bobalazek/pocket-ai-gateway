package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store.SystemDB())
}

func TestRoutesKeepDashboardAndAPIErrorsSeparate(t *testing.T) {
	handler := testHandler(t)

	tests := []struct {
		path        string
		status      int
		contentType string
		contains    string
	}{
		{path: "/_/", status: http.StatusOK, contentType: "text/html", contains: "Pocket AI Gateway"},
		{path: "/_/status/?id=request_123", status: http.StatusOK, contentType: "text/html", contains: "Runtime status"},
		{path: "/_/missing/", status: http.StatusNotFound, contentType: "text/html", contains: "Page not found"},
		{path: "/llms.txt", status: http.StatusOK, contentType: "text/plain", contains: "Pocket AI Gateway"},
		{path: "/api/openai/v1/missing", status: http.StatusNotFound, contentType: "application/json", contains: "not_found"},
		{path: "/api/anthropic/v1/missing", status: http.StatusNotFound, contentType: "application/json", contains: "not_found"},
		{path: "/api/gemini/v1beta/missing", status: http.StatusNotFound, contentType: "application/json", contains: "not_found"},
		{path: "/v1/missing", status: http.StatusNotFound, contentType: "application/json", contains: "not_found"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if got := response.Header().Get("Content-Type"); !strings.Contains(got, test.contentType) {
				t.Fatalf("content type = %q, want %q", got, test.contentType)
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body does not contain %q", test.contains)
			}
		})
	}
}

func TestDashboardCSPAllowsOnlyItsExportedInlineScripts(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/_/", nil))

	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "'sha256-") || strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("unexpected dashboard CSP: %q", csp)
	}
}

func TestConfiguredPublicOriginRejectsOtherHosts(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithOrigin(store.SystemDB(), "https://gateway.example.test")
	request := httptest.NewRequest(http.MethodGet, "http://other.example.test/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("untrusted host status = %d", response.Code)
	}
}
