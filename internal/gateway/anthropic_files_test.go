package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestAnthropicFilesLifecycleAndMessages(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		if request.URL.Path == "/v1/messages/count_tokens" {
			_, _ = io.WriteString(response, `{"input_tokens":42}`)
			return
		}
		_, _ = io.WriteString(response, `{"id":"msg_file","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":42,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat", "count_tokens"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic files", Scopes: []string{"files:manage", "chat:generate", "tokens:count"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{"files:manage", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, noFiles, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No files", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{7}, 32)).Register(mux)
	content := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	created := performAnthropicFileUpload(t, mux, secret, "document.pdf", "application/pdf", content, "3600")
	if created.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", created.Code, created.Body.String())
	}
	var file anthropicFile
	if json.Unmarshal(created.Body.Bytes(), &file) != nil || !strings.HasPrefix(file.ID, "file_") || file.MimeType != "application/pdf" || file.Downloadable || file.SizeBytes != int64(len(content)) || file.Type != "file" {
		t.Fatalf("upload metadata=%s", created.Body.String())
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, file.CreatedAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, file.ExpiresAt)
	if expiresAt.Sub(createdAt) != time.Hour {
		t.Fatalf("expiration=%q to %q", file.CreatedAt, file.ExpiresAt)
	}
	var ciphertext []byte
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT ciphertext FROM anthropic_files WHERE id=? AND key_id=?`, file.ID, key.ID).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, content) {
		t.Fatalf("encrypted storage error=%v", err)
	}
	base := "/api/anthropic/v1/files/" + file.ID
	for _, path := range []string{base, base + "/content"} {
		if result := performAnthropicFileRequest(t, mux, http.MethodGet, path, other, nil, ""); result.Code != http.StatusNotFound {
			t.Fatalf("cross-key path=%s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodGet, base, secret, nil, ""); result.Code != http.StatusOK {
		t.Fatalf("metadata status=%d body=%s", result.Code, result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodGet, base+"/content", secret, nil, ""); result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "not downloadable") {
		t.Fatalf("download status=%d body=%s", result.Code, result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodGet, "/api/anthropic/v1/files", secret, nil, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), file.ID) || !strings.Contains(result.Body.String(), `"next_page":null`) {
		t.Fatalf("list status=%d body=%s", result.Code, result.Body.String())
	}
	message := fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":%q}}]}]}`, model.ID, file.ID)
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", other, strings.NewReader(message), "application/json"); result.Code != http.StatusNotFound || calls.Load() != 0 {
		t.Fatalf("foreign reference status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", noFiles, strings.NewReader(message), "application/json"); result.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("unscoped reference status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	result := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(message), "application/json")
	if result.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("message status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	var sent map[string]any
	if json.Unmarshal(forwarded, &sent) != nil {
		t.Fatalf("forwarded=%s", forwarded)
	}
	block := sent["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	source := block["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != base64.StdEncoding.EncodeToString(content) || source["file_id"] != nil {
		t.Fatalf("forwarded source=%v", source)
	}
	textFileResponse := performAnthropicFileUpload(t, mux, secret, "notes.txt", "text/plain", []byte("hello notes"), "")
	var textFile anthropicFile
	if textFileResponse.Code != http.StatusOK || json.Unmarshal(textFileResponse.Body.Bytes(), &textFile) != nil {
		t.Fatalf("text upload status=%d body=%s", textFileResponse.Code, textFileResponse.Body.String())
	}
	textMessage := fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":%q}}]}]}`, model.ID, textFile.ID)
	textResult := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(textMessage), "application/json")
	if textResult.Code != http.StatusOK || calls.Load() != 2 || json.Unmarshal(forwarded, &sent) != nil {
		t.Fatalf("text message status=%d calls=%d body=%s", textResult.Code, calls.Load(), textResult.Body.String())
	}
	textSource := sent["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["source"].(map[string]any)
	if textSource["type"] != "text" || textSource["media_type"] != "text/plain" || textSource["data"] != "hello notes" {
		t.Fatalf("forwarded text source=%v", textSource)
	}
	counted := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/count_tokens", secret, strings.NewReader(message), "application/json")
	if counted.Code != http.StatusOK || calls.Load() != 3 || !strings.Contains(counted.Body.String(), `"input_tokens":42`) {
		t.Fatalf("count tokens status=%d calls=%d body=%s", counted.Code, calls.Load(), counted.Body.String())
	}
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "body_bytes", Algorithm: "ceiling", LimitUnits: int64(len(message) + 10)}); err != nil {
		t.Fatal(err)
	}
	denied := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(message), "application/json")
	if denied.Code != http.StatusTooManyRequests || calls.Load() != 3 {
		t.Fatalf("expanded body ceiling status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodDelete, base, secret, nil, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"file_deleted"`) {
		t.Fatalf("delete status=%d body=%s", result.Code, result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(message), "application/json"); result.Code != http.StatusNotFound || calls.Load() != 3 {
		t.Fatalf("deleted reference status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE anthropic_files SET created_at=?,expires_at=? WHERE id=?`, now-2*int64(time.Hour/time.Millisecond), now-int64(time.Hour/time.Millisecond), textFile.ID); err != nil {
		t.Fatal(err)
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(textMessage), "application/json"); result.Code != http.StatusNotFound || calls.Load() != 3 {
		t.Fatalf("expired reference status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodDelete, "/api/anthropic/v1/files/"+textFile.ID, secret, nil, ""); result.Code != http.StatusNotFound {
		t.Fatalf("expired delete status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestAnthropicFilesValidateUploadsAndPagination(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Files", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{8}, 32)).Register(mux)
	for index, value := range []string{"one", "two", "three"} {
		result := performAnthropicFileUpload(t, mux, secret, fmt.Sprintf("%d.txt", index), "text/plain", []byte(value), "")
		if result.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", result.Code, result.Body.String())
		}
	}
	first := performAnthropicFileRequest(t, mux, http.MethodGet, "/api/anthropic/v1/files?limit=1", secret, nil, "")
	var page struct {
		Data     []anthropicFile `json:"data"`
		NextPage *string         `json:"next_page"`
	}
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Data) != 1 || page.NextPage == nil {
		t.Fatalf("first page=%d %s", first.Code, first.Body.String())
	}
	second := performAnthropicFileRequest(t, mux, http.MethodGet, "/api/anthropic/v1/files?limit=1&page="+*page.NextPage, secret, nil, "")
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Data) != 1 {
		t.Fatalf("second page=%d %s", second.Code, second.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=101", "page=page_missing", "ids%5B%5D=some-id", "limit=1&limit=2"} {
		result := performAnthropicFileRequest(t, mux, http.MethodGet, "/api/anthropic/v1/files?"+query, secret, nil, "")
		if result.Code != http.StatusBadRequest {
			t.Fatalf("invalid query=%q status=%d body=%s", query, result.Code, result.Body.String())
		}
	}
	for _, test := range []struct {
		filename, mime, value, expires string
		status                         int
	}{
		{"wrong.pdf", "application/pdf", "plain text", "", http.StatusBadRequest},
		{"bad\tname.txt", "text/plain", "hello", "", http.StatusBadRequest},
		{"binary.bin", "application/octet-stream", "\x00\x01", "", http.StatusBadRequest},
		{"data.txt", "text/plain", "hello", "3599", http.StatusBadRequest},
		{"data.txt", "text/plain", "hello", "7776001", http.StatusBadRequest},
		{"data.txt", "text/plain", "hello", "3600", http.StatusOK},
	} {
		result := performAnthropicFileUpload(t, mux, secret, test.filename, test.mime, []byte(test.value), test.expires)
		if result.Code != test.status {
			t.Fatalf("upload %q status=%d body=%s", test.filename, result.Code, result.Body.String())
		}
	}
	legacyReq := httptest.NewRequest(http.MethodGet, "/api/anthropic/v1/files", nil)
	legacyReq.Header.Set("x-api-key", secret)
	legacyReq.Header.Set("anthropic-version", "2023-06-01")
	legacyReq.Header.Set("anthropic-beta", "files-api-2025-04-14")
	legacy := httptest.NewRecorder()
	mux.ServeHTTP(legacy, legacyReq)
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy beta status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	large := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("0"), 7<<20)...)
	largeUpload := performAnthropicFileUpload(t, mux, secret, "large.pdf", "application/pdf", large, "")
	var largeFile anthropicFile
	if largeUpload.Code != http.StatusOK || json.Unmarshal(largeUpload.Body.Bytes(), &largeFile) != nil {
		t.Fatalf("large upload status=%d body=%s", largeUpload.Code, largeUpload.Body.String())
	}
	message := fmt.Sprintf(`{"model":"unrouted","max_tokens":16,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":%q}},{"type":"document","source":{"type":"file","file_id":%q}}]}]}`, largeFile.ID, largeFile.ID)
	oversized := performAnthropicFileRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages", secret, strings.NewReader(message), "application/json")
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expanded size status=%d body=%s", oversized.Code, oversized.Body.String())
	}
}

func TestAnthropicFilesNestedReferencesStayKeyScopedAndNative(t *testing.T) {
	var anthropicCalls, openAICalls atomic.Int64
	var forwarded []byte
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		anthropicCalls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		_, _ = io.WriteString(response, `{"id":"msg_nested","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer anthropicUpstream.Close()
	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		openAICalls.Add(1)
		_, _ = io.WriteString(response, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"unexpected"},"finish_reason":"stop"}]}`)
	}))
	defer openAIUpstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	anthropicConnection, anthropicModel := publishModel(t, ctx, providerService, owner, "anthropic", anthropicUpstream.URL+"/v1", "claude-upstream", []string{"chat"})
	openAIConnection, openAIModel := publishModel(t, ctx, providerService, owner, "openai", openAIUpstream.URL+"/v1", "gpt-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Nested Files", Scopes: []string{"files:manage", "chat:generate"}, ModelPatterns: []string{anthropicModel.ID, openAIModel.ID}, ConnectionIDs: []string{anthropicConnection.ID, openAIConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{9}, 32)).Register(mux)
	upload := performAnthropicFileUpload(t, mux, secret, "nested.txt", "text/plain", []byte("nested document"), "")
	var file anthropicFile
	if upload.Code != http.StatusOK || json.Unmarshal(upload.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", upload.Code, upload.Body.String())
	}
	nestedMessage := func(model, id string) string {
		return fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"document","source":{"type":"file","file_id":%q}}]}]}]}`, model, id)
	}
	path := "/api/anthropic/v1/messages"
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(nestedMessage(anthropicModel.ID, "file_upstream_workspace")), "application/json"); result.Code != http.StatusNotFound || anthropicCalls.Load() != 0 {
		t.Fatalf("raw nested ID status=%d upstream=%d body=%s", result.Code, anthropicCalls.Load(), result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(nestedMessage(anthropicModel.ID, file.ID)), "application/json"); result.Code != http.StatusOK || anthropicCalls.Load() != 1 {
		t.Fatalf("nested local ID status=%d upstream=%d body=%s", result.Code, anthropicCalls.Load(), result.Body.String())
	}
	var sent map[string]any
	if json.Unmarshal(forwarded, &sent) != nil {
		t.Fatalf("forwarded=%s", forwarded)
	}
	nestedSource := sent["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["source"].(map[string]any)
	if nestedSource["type"] != "text" || nestedSource["data"] != "nested document" || nestedSource["file_id"] != nil {
		t.Fatalf("nested source=%v", nestedSource)
	}
	for _, block := range []string{`{"type":"bash_code_execution_output","file_id":"file_upstream_workspace"}`, `{"type":"container_upload","file_id":"file_upstream_workspace"}`} {
		message := fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[%s]}]}]}`, anthropicModel.ID, block)
		if result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(message), "application/json"); result.Code != http.StatusBadRequest || anthropicCalls.Load() != 1 {
			t.Fatalf("unsupported nested ID status=%d upstream=%d body=%s", result.Code, anthropicCalls.Load(), result.Body.String())
		}
	}
	unrelated := fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"file_id":"application-data"}}]}]}`, anthropicModel.ID)
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(unrelated), "application/json"); result.Code != http.StatusOK || anthropicCalls.Load() != 2 {
		t.Fatalf("unrelated input status=%d upstream=%d body=%s", result.Code, anthropicCalls.Load(), result.Body.String())
	}
	if result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(nestedMessage(openAIModel.ID, file.ID)), "application/json"); result.Code != http.StatusNotFound || openAICalls.Load() != 0 {
		t.Fatalf("translated file status=%d openai=%d body=%s", result.Code, openAICalls.Load(), result.Body.String())
	}
}

func performAnthropicFileUpload(t *testing.T, handler http.Handler, secret, filename, mediaType string, content []byte, expires string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(http.Header)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(textproto.MIMEHeader(header))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if expires != "" {
		if err := writer.WriteField("expires_in_seconds", expires); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return performAnthropicFileRequest(t, handler, http.MethodPost, "/api/anthropic/v1/files", secret, &body, writer.FormDataContentType())
}

func performAnthropicFileRequest(t *testing.T, handler http.Handler, method, path, secret string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
