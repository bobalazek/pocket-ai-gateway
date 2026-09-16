package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestOpenAIFileLifecycleEncryptionAndKeyIsolation(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No files", Scopes: []string{"chat:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{7}, 32)
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, masterKey).Register(mux)
	content := []byte("{\"custom_id\":\"first\"}\n")
	created := performFileUpload(t, mux, secret, "input.jsonl", content, map[string]string{"purpose": "batch", "expires_after[anchor]": "created_at", "expires_after[seconds]": "3600"}, nil)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var file openAIFile
	if json.Unmarshal(created.Body.Bytes(), &file) != nil || !strings.HasPrefix(file.ID, "file_") || file.Object != "file" || file.Bytes != int64(len(content)) || file.Filename != "input.jsonl" || file.Purpose != "batch" || file.Status != "processed" || file.ExpiresAt-file.CreatedAt != 3600 {
		t.Fatalf("created=%s", created.Body.String())
	}
	var ciphertext, nonce []byte
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT ciphertext,nonce FROM openai_files WHERE id=? AND key_id=?`, file.ID, key.ID).Scan(&ciphertext, &nonce); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, content) || len(ciphertext) != len(content)+16 || len(nonce) != 12 {
		t.Fatalf("stored ciphertext=%d nonce=%d", len(ciphertext), len(nonce))
	}
	plain, err := credentials.Open(masterKey, ciphertext, nonce, fileAdditionalData(file.ID, key.ID, "batch", int64(len(content))))
	if err != nil || !bytes.Equal(plain, content) {
		t.Fatalf("decrypt=%q err=%v", plain, err)
	}
	for _, path := range []string{"/api/openai/v1/files/" + file.ID, "/api/openai/v1/files/" + file.ID + "/content"} {
		if result := performFileRequest(t, mux, http.MethodGet, path, otherSecret, nil, ""); result.Code != http.StatusNotFound {
			t.Fatalf("cross-key %s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
	if result := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID, deniedSecret, nil, ""); result.Code != http.StatusForbidden {
		t.Fatalf("unscoped status=%d body=%s", result.Code, result.Body.String())
	}
	retrieved := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID, secret, nil, "")
	if retrieved.Code != http.StatusOK || !strings.Contains(retrieved.Body.String(), file.ID) {
		t.Fatalf("retrieve status=%d body=%s", retrieved.Code, retrieved.Body.String())
	}
	downloaded := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID+"/content", secret, nil, "")
	if downloaded.Code != http.StatusOK || !bytes.Equal(downloaded.Body.Bytes(), content) || downloaded.Header().Get("Content-Type") != "application/octet-stream" || downloaded.Header().Get("Cache-Control") != "no-store" || downloaded.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(downloaded.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(downloaded.Header().Get("Content-Disposition"), "input.jsonl") {
		t.Fatalf("download status=%d headers=%v body=%q", downloaded.Code, downloaded.Header(), downloaded.Body.Bytes())
	}
	listed := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files?purpose=batch&limit=1&order=desc", secret, nil, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), file.ID) || !strings.Contains(listed.Body.String(), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	deleted := performFileRequest(t, mux, http.MethodDelete, "/api/openai/v1/files/"+file.ID, secret, nil, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if missing := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID, secret, nil, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("post-delete status=%d", missing.Code)
	}
	empty := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files", secret, nil, "")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"first_id":null`) || !strings.Contains(empty.Body.String(), `"last_id":null`) {
		t.Fatalf("empty list status=%d body=%s", empty.Code, empty.Body.String())
	}
}

func TestOpenAIFileListUsesValidatedKeysetPagination(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{3}, 32)).Register(mux)
	ids := make([]string, 0, 3)
	for _, name := range []string{"one.jsonl", "two.jsonl", "three.jsonl"} {
		result := performFileUpload(t, mux, secret, name, []byte("{}\n"), map[string]string{"purpose": "batch"}, nil)
		if result.Code != http.StatusOK {
			t.Fatalf("create %s status=%d body=%s", name, result.Code, result.Body.String())
		}
		var file openAIFile
		_ = json.Unmarshal(result.Body.Bytes(), &file)
		ids = append(ids, file.ID)
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_files SET created_at=?,expires_at=?`, now, now+int64(defaultFileExpiry/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	first := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files?order=asc&limit=1", secret, nil, "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), ids[0]) || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("first status=%d body=%s ids=%v", first.Code, first.Body.String(), ids)
	}
	second := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files?order=asc&limit=1&after="+url.QueryEscape(ids[0]), secret, nil, "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), ids[1]) || strings.Contains(second.Body.String(), ids[0]) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	for _, path := range []string{"/api/openai/v1/files?limit=0", "/api/openai/v1/files?limit=101", "/api/openai/v1/files?order=newest", "/api/openai/v1/files?purpose=invalid", "/api/openai/v1/files?after=missing", "/api/openai/v1/files?after=a&after=b"} {
		result := performFileRequest(t, mux, http.MethodGet, path, secret, nil, "")
		if result.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
}

func TestOpenAIFileUploadValidationAndBounds(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{9}, 32)).Register(mux)
	tests := []struct {
		name     string
		filename string
		content  []byte
		fields   map[string]string
		extra    []filePart
		status   int
	}{
		{"missing-purpose", "input.jsonl", []byte("{}"), nil, nil, http.StatusBadRequest},
		{"reserved-purpose", "input.jsonl", []byte("{}"), map[string]string{"purpose": "batch_output"}, nil, http.StatusBadRequest},
		{"extension", "input.txt", []byte("{}"), map[string]string{"purpose": "batch"}, nil, http.StatusBadRequest},
		{"assistants", "notes.txt", []byte("notes"), map[string]string{"purpose": "assistants"}, nil, http.StatusOK},
		{"fine-tune", "training.jsonl", []byte("{}\n"), map[string]string{"purpose": "fine-tune"}, nil, http.StatusOK},
		{"vision", "image.png", []byte("image"), map[string]string{"purpose": "vision"}, nil, http.StatusOK},
		{"user-data", "document.pdf", []byte("document"), map[string]string{"purpose": "user_data"}, nil, http.StatusOK},
		{"evals", "evals.jsonl", []byte("{}\n"), map[string]string{"purpose": "evals"}, nil, http.StatusOK},
		{"evals-extension", "evals.txt", []byte("{}\n"), map[string]string{"purpose": "evals"}, nil, http.StatusBadRequest},
		{"empty", "input.jsonl", nil, map[string]string{"purpose": "batch"}, nil, http.StatusBadRequest},
		{"partial-expiry", "input.jsonl", []byte("{}"), map[string]string{"purpose": "batch", "expires_after[seconds]": "3600"}, nil, http.StatusBadRequest},
		{"bad-anchor", "input.jsonl", []byte("{}"), map[string]string{"purpose": "batch", "expires_after[anchor]": "modified_at", "expires_after[seconds]": "3600"}, nil, http.StatusBadRequest},
		{"short-expiry", "input.jsonl", []byte("{}"), map[string]string{"purpose": "batch", "expires_after[anchor]": "created_at", "expires_after[seconds]": "3599"}, nil, http.StatusBadRequest},
		{"extra-upload", "input.jsonl", []byte("{}"), map[string]string{"purpose": "batch"}, []filePart{{name: "other", filename: "other.jsonl", content: []byte("{}")}}, http.StatusBadRequest},
		{"oversized", "input.jsonl", bytes.Repeat([]byte{'x'}, maxFileBytes+1), map[string]string{"purpose": "batch"}, nil, http.StatusRequestEntityTooLarge},
		{"exact-limit", "input.jsonl", bytes.Repeat([]byte{'x'}, maxFileBytes), map[string]string{"purpose": "batch"}, nil, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := performFileUpload(t, mux, secret, test.filename, test.content, test.fields, test.extra)
			if result.Code != test.status {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			if test.status == http.StatusOK {
				var item openAIFile
				if err := json.Unmarshal(result.Body.Bytes(), &item); err != nil || item.Purpose != test.fields["purpose"] {
					t.Fatalf("purpose=%q error=%v body=%s", item.Purpose, err, result.Body.String())
				}
			}
		})
	}
}

func TestOpenAIFilesShareRetainedResourceCapacity(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for index := 0; index < retainedKeyJobs; index++ {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,0,zeroblob(16),zeroblob(12),?,?)`, "file_capacity_"+strconv.Itoa(index), owner.ID, key.ID, "input.jsonl", "batch", now, now+int64(time.Hour/time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{2}, 32)).Register(mux)
	result := performFileUpload(t, mux, secret, "blocked.jsonl", []byte("{}"), map[string]string{"purpose": "batch"}, nil)
	if result.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	var count int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_files WHERE key_id=?`, key.ID).Scan(&count); err != nil || count != retainedKeyJobs {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestOpenAIFileDownloadsAreBounded(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{5}, 32)).Register(mux)
	created := performFileUpload(t, mux, secret, "input.jsonl", []byte("{}\n"), map[string]string{"purpose": "batch"}, nil)
	var file openAIFile
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &file) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}

	blocked := &blockingFileWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodGet, "/api/openai/v1/files/"+file.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	done := make(chan struct{})
	go func() { defer close(done); mux.ServeHTTP(blocked, request) }()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("first download did not start")
	}
	second := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID+"/content", secret, nil, "")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second download status=%d body=%s", second.Code, second.Body.String())
	}
	close(blocked.release)
	<-done
}

func TestGatewayFileReferencesAreRejectedBeforeInferenceDispatch(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Inference", Scopes: []string{"chat:generate", "responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	requests := []struct{ path, body string }{
		{"/api/openai/v1/responses", `{"model":"` + model.ID + `","store":false,"input":[{"type":"input_file","file_id":"file_local"}]}`},
		{"/api/openai/v1/chat/completions", `{"model":"` + model.ID + `","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_local"}}]}]}`},
	}
	for _, test := range requests {
		result := performFileRequest(t, mux, http.MethodPost, test.path, secret, strings.NewReader(test.body), "application/json")
		if result.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", test.path, result.Code, result.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
	if containsLocalFileReference(json.RawMessage(`[{"type":"function","parameters":{"properties":{"file_id":{"type":"string"}}}}]`)) {
		t.Fatal("function schema was mistaken for a local file reference")
	}
}

type filePart struct {
	name, filename string
	content        []byte
}

type blockingFileWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
}

func (writer *blockingFileWriter) Header() http.Header { return writer.header }
func (writer *blockingFileWriter) WriteHeader(int)     {}
func (writer *blockingFileWriter) Write(value []byte) (int, error) {
	close(writer.started)
	<-writer.release
	return len(value), nil
}

func performFileUpload(t *testing.T, handler http.Handler, secret, filename string, content []byte, fields map[string]string, extra []filePart) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(content)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range extra {
		part, err := writer.CreateFormFile(item.name, item.filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(item.content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return performFileRequest(t, handler, http.MethodPost, "/api/openai/v1/files", secret, &body, writer.FormDataContentType())
}

func performFileRequest(t *testing.T, handler http.Handler, method, path, secret string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+secret)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
