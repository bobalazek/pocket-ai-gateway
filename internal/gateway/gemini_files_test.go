package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestGeminiFilesUploadAndKeyOwnedResolution(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Gemini Files", Scopes: []string{"files:manage", "chat:generate", "tokens:count"}})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other key", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, noFiles, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No Files", Scopes: []string{"chat:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{7}, 32))
	mux := http.NewServeMux()
	handler.Register(mux)
	call := func(method, path, key, command, offset string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if key != "" {
			r.Header.Set("x-goog-api-key", key)
		}
		if command != "" {
			r.Header.Set("X-Goog-Upload-Command", command)
		}
		if offset != "" {
			r.Header.Set("X-Goog-Upload-Offset", offset)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	start := func(key string, size int, metadata ...string) *httptest.ResponseRecorder {
		body := `{"file":{"displayName":"note.txt","mimeType":"text/plain","sizeBytes":"4"}}`
		if len(metadata) > 0 {
			body = metadata[0]
		}
		r := httptest.NewRequest(http.MethodPost, "/api/gemini/upload/v1beta/files", strings.NewReader(body))
		r.Header.Set("x-goog-api-key", key)
		r.Header.Set("X-Goog-Upload-Protocol", "resumable")
		r.Header.Set("X-Goog-Upload-Command", "start")
		r.Header.Set("X-Goog-Upload-Header-Content-Length", "4")
		r.Header.Set("X-Goog-Upload-Header-Content-Type", "text/plain")
		if size != 4 {
			r.Header.Set("X-Goog-Upload-Header-Content-Length", "8388609")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if response := start(noFiles, 4); response.Code != 403 {
		t.Fatalf("no scope: %d %s", response.Code, response.Body)
	}
	if response := start(secret, maxGeminiFileBytes+1); response.Code != 413 {
		t.Fatalf("oversize: %d %s", response.Code, response.Body)
	}
	if response := start(secret, 4, `{"file":{"mimeType":"text/plain","mimeType":"text/plain"}}`); response.Code != 400 {
		t.Fatalf("duplicate metadata: %d %s", response.Code, response.Body)
	}
	deep := `{"fileData":{"fileUri":"pag-gemini://files/missing"}}`
	for range 70 {
		deep = `{"nested":` + deep + `}`
	}
	if response := call("POST", "/api/gemini/v1beta/models/assistant:generateContent", secret, "", "", []byte(deep)); response.Code != 400 {
		t.Fatalf("deep unsupported file reference: %d %s", response.Code, response.Body)
	}
	duplicate := `{"contents":[{"parts":[{"text":"safe"}],"parts":[{"fileData":{"fileUri":"pag-gemini://files/missing"}}]}]}`
	if response := call("POST", "/api/gemini/v1beta/models/assistant:generateContent", secret, "", "", []byte(duplicate)); response.Code != 400 {
		t.Fatalf("duplicate inference fields: %d %s", response.Code, response.Body)
	}
	var admitted int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&admitted); err != nil || admitted != 0 {
		t.Fatalf("deep reference reached admission: %d %v", admitted, err)
	}
	started := start(secret, 4)
	if started.Code != 200 {
		t.Fatalf("start: %d %s", started.Code, started.Body)
	}
	uploadURL, err := url.Parse(started.Header().Get("X-Goog-Upload-URL"))
	if err != nil || uploadURL.Path == "" {
		t.Fatalf("upload URL: %q %v", started.Header().Get("X-Goog-Upload-URL"), err)
	}
	if response := call("POST", uploadURL.Path, other, "upload, finalize", "0", []byte("abcd")); response.Code != 404 {
		t.Fatalf("other key upload: %d %s", response.Code, response.Body)
	}
	if response := call("POST", uploadURL.Path, "", "upload", "0", []byte("ab")); response.Code != 200 || response.Header().Get("X-Goog-Upload-Status") != "active" {
		t.Fatalf("first chunk: %d %s", response.Code, response.Body)
	}
	if response := call("POST", uploadURL.Path, "", "upload", "0", []byte("ab")); response.Code != 200 || response.Header().Get("X-Goog-Upload-Status") != "active" {
		t.Fatalf("retried chunk: %d %s", response.Code, response.Body)
	}
	if response := call("POST", uploadURL.Path, secret, "upload, finalize", "0", []byte("cd")); response.Code != 409 {
		t.Fatalf("wrong offset: %d %s", response.Code, response.Body)
	}
	final := call("POST", uploadURL.Path, "", "upload, finalize", "2", []byte("cd"))
	if final.Code != 200 || final.Header().Get("X-Goog-Upload-Status") != "final" {
		t.Fatalf("final chunk: %d %s", final.Code, final.Body)
	}
	var decoded struct {
		File struct {
			Name, URI, MIMEType, State string
			SizeBytes                  string
		}
	}
	if err := json.Unmarshal(final.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if retry := call("POST", uploadURL.Path, "", "upload, finalize", "2", []byte("cd")); retry.Code != 200 || retry.Body.String() != final.Body.String() {
		t.Fatalf("retried finalize: %d %s", retry.Code, retry.Body)
	}
	keyID := mustGeminiPrincipal(t, keyService, secret).KeyID
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, keyID, retainedKeyJobs-1, 0); err != nil {
		t.Fatalf("completed upload double-counted against key retention: %v", err)
	}
	if decoded.File.URI == "" || decoded.File.MIMEType != "text/plain" || decoded.File.SizeBytes != "4" || decoded.File.State != "ACTIVE" {
		t.Fatalf("file: %+v", decoded.File)
	}
	id := strings.TrimPrefix(decoded.File.Name, "files/")
	var ciphertext []byte
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT ciphertext FROM gemini_files WHERE id=?`, id).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("abcd")) {
		t.Fatalf("unencrypted file or query error: %v", err)
	}
	if response := call("GET", "/api/gemini/v1beta/files/"+id, other, "", "", nil); response.Code != 404 {
		t.Fatalf("other key get: %d %s", response.Code, response.Body)
	}
	if response := call("GET", "/api/gemini/v1beta/files/"+id, secret, "", "", nil); response.Code != 200 {
		t.Fatalf("get: %d %s", response.Code, response.Body)
	}
	if response := call("GET", "/api/gemini/v1beta/files", secret, "", "", nil); response.Code != 200 || !strings.Contains(response.Body.String(), decoded.File.Name) {
		t.Fatalf("list: %d %s", response.Code, response.Body)
	}
	request := []byte(`{"contents":[{"parts":[{"fileData":{"fileUri":"` + decoded.File.URI + `","mimeType":"text/plain"}}]}]}`)
	expanded, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "generateContent", true, request)
	if err != nil || !bytes.Contains(expanded, []byte(base64.StdEncoding.EncodeToString([]byte("abcd")))) || bytes.Contains(expanded, []byte("fileData")) {
		t.Fatalf("expanded: %s %v", expanded, err)
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "generateContent", false, request); err == nil {
		t.Fatal("file reference worked after Files scope was removed")
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, other).KeyID, "countTokens", true, request); err == nil {
		t.Fatal("other key resolved file")
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "interactions", true, request); err == nil {
		t.Fatal("interaction accepted file")
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "generateContent", true, []byte(`{"systemInstruction":{"parts":[{"fileData":{"fileUri":"`+decoded.File.URI+`"}}]}}`)); err == nil {
		t.Fatal("system instruction accepted file")
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "generateContent", true, []byte(`{"contents":[{"parts":[{"fileData":{"fileUri":"`+decoded.File.URI+`","nested":{"fileData":{}}}}]}]}`)); err == nil {
		t.Fatal("nested file data accepted")
	}
	largeID, large := "aggregate-test", bytes.Repeat([]byte("x"), 5<<20)
	largeCiphertext, largeNonce, err := credentials.Seal(handler.masterKey, large, geminiFileAAD(largeID, keyID, "text/plain", int64(len(large))))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO gemini_files(id,owner_user_id,key_id,display_name,mime_type,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, largeID, owner.ID, keyID, "large.txt", "text/plain", len(large), largeCiphertext, largeNonce, now, now+geminiFileLifetime.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	uri := geminiFileURI(largeID)
	oversized := []byte(`{"contents":[{"parts":[{"fileData":{"fileUri":"` + uri + `"}},{"fileData":{"fileUri":"` + uri + `"}}]}]}`)
	if _, err := handler.resolveGeminiFileReferences(ctx, keyID, "generateContent", true, oversized); err == nil {
		t.Fatal("aggregate referenced files exceeded 8 MiB")
	}
	if response := call("DELETE", "/api/gemini/v1beta/files/"+id, other, "", "", nil); response.Code != 404 {
		t.Fatalf("other key delete: %d %s", response.Code, response.Body)
	}
	if response := call("DELETE", "/api/gemini/v1beta/files/"+id, secret, "", "", nil); response.Code != 200 {
		t.Fatalf("delete: %d %s", response.Code, response.Body)
	}
	if _, err := handler.resolveGeminiFileReferences(ctx, mustGeminiPrincipal(t, keyService, secret).KeyID, "generateContent", true, request); err == nil {
		t.Fatal("deleted file resolved")
	}
}

func mustGeminiPrincipal(t *testing.T, service *keys.Service, secret string) keys.Principal {
	t.Helper()
	principal, err := service.Authenticate(t.Context(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
