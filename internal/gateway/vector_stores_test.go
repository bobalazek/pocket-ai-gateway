package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestOpenAIVectorStoreLifecycleAndKeyIsolation(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Stores", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other Vector Stores", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No Vector Stores", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)

	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Knowledge","description":"Product docs","metadata":{"suite":"gateway"},"expires_after":{"anchor":"last_active_at","days":1},"file_ids":[]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var item vectorStore
	if json.Unmarshal(created.Body.Bytes(), &item) != nil || !strings.HasPrefix(item.ID, "vs_") || item.Object != "vector_store" || item.Name != "Knowledge" || item.Status != "completed" || item.Metadata["suite"] != "gateway" || item.ExpiresAfter == nil || item.ExpiresAfter.Days != 1 || item.ExpiresAt == nil || item.LastActiveAt == nil || *item.ExpiresAt-*item.LastActiveAt != 86400 {
		t.Fatalf("created=%s", created.Body.String())
	}
	if result := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+item.ID, otherSecret, ""); result.Code != http.StatusNotFound {
		t.Fatalf("cross-key status=%d body=%s", result.Code, result.Body.String())
	}
	if result := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores", deniedSecret, ""); result.Code != http.StatusForbidden {
		t.Fatalf("unscoped status=%d body=%s", result.Code, result.Body.String())
	}
	updated := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+item.ID, secret, `{"name":null,"metadata":{"suite":"updated"},"expires_after":null}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	var changed vectorStore
	if json.Unmarshal(updated.Body.Bytes(), &changed) != nil || changed.Name != "" || changed.Metadata["suite"] != "updated" || changed.ExpiresAfter != nil || changed.ExpiresAt != nil {
		t.Fatalf("updated=%s", updated.Body.String())
	}
	if result := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+item.ID, secret, ""); result.Code != http.StatusOK {
		t.Fatalf("retrieve status=%d body=%s", result.Code, result.Body.String())
	}
	deleted := performVectorStoreRequest(t, mux, http.MethodDelete, "/api/openai/v1/vector_stores/"+item.ID, secret, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"object":"vector_store.deleted"`) || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if result := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+item.ID, secret, ""); result.Code != http.StatusNotFound {
		t.Fatalf("post-delete status=%d body=%s", result.Code, result.Body.String())
	}
	var count int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_stores WHERE key_id=?`, key.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestOpenAIVectorStoreListUsesValidatedKeysetPagination(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Stores", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	ids := make([]string, 0, 3)
	for _, name := range []string{"One", "Two", "Three"} {
		result := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"`+name+`"}`)
		if result.Code != http.StatusOK {
			t.Fatalf("create status=%d body=%s", result.Code, result.Body.String())
		}
		var item vectorStore
		_ = json.Unmarshal(result.Body.Bytes(), &item)
		ids = append(ids, item.ID)
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_vector_stores SET created_at=?,last_active_at=?`, now, now); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	first := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores?order=asc&limit=1", secret, "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), ids[0]) || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores?order=asc&limit=1&after="+url.QueryEscape(ids[0]), secret, "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), ids[1]) || strings.Contains(second.Body.String(), ids[0]) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	previous := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores?order=asc&limit=1&before="+url.QueryEscape(ids[1]), secret, "")
	if previous.Code != http.StatusOK || !strings.Contains(previous.Body.String(), ids[0]) {
		t.Fatalf("previous status=%d body=%s", previous.Code, previous.Body.String())
	}
	for _, path := range []string{"/api/openai/v1/vector_stores?limit=0", "/api/openai/v1/vector_stores?order=newest", "/api/openai/v1/vector_stores?after=missing", "/api/openai/v1/vector_stores?after=a&before=b", "/api/openai/v1/vector_stores?before=a&before=b"} {
		result := performVectorStoreRequest(t, mux, http.MethodGet, path, secret, "")
		if result.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
}

func TestOpenAIVectorStoreValidationAndExpiry(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Stores", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	tests := []struct {
		body string
		code string
	}{
		{`{"chunking_strategy":{"type":"auto"}}`, "invalid_request"},
		{`{"file_ids":[],"chunking_strategy":{"type":"auto"}}`, "invalid_request"},
		{`{"file_ids":["file_local"],"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`, "unsupported_feature"},
		{`{"file_ids":null}`, "invalid_request"},
		{`{"expires_after":{"anchor":"created_at","days":1}}`, "invalid_request"},
		{`{"expires_after":{"anchor":"last_active_at","days":366}}`, "invalid_request"},
		{`{"metadata":{"bad":true}}`, "invalid_request"},
		{`{"unknown":true}`, "invalid_request"},
	}
	for _, test := range tests {
		result := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, test.body)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("body=%s status=%d response=%s", test.body, result.Code, result.Body.String())
		}
	}
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Expiring","expires_after":{"anchor":"last_active_at","days":1}}`)
	var item vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &item) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_vector_stores SET created_at=0,last_active_at=0,expires_at=86400000 WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if result := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+item.ID, secret, ""); result.Code != http.StatusNotFound {
		t.Fatalf("expired status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestOpenAIVectorStoresShareRetainedResourceCapacity(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Stores", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for index := 0; index < retainedKeyJobs; index++ {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,0,zeroblob(16),zeroblob(12),?,?)`, "file_vector_capacity_"+strconv.Itoa(index), owner.ID, key.ID, "input.jsonl", "batch", now, now+int64(time.Hour/time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	result := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Blocked"}`)
	if result.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
}

func performVectorStoreRequest(t *testing.T, handler http.Handler, method, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *strings.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	} else {
		requestBody = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, requestBody)
	request.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
