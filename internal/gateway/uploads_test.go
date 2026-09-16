package gateway

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestOpenAIUploadLifecycleEncryptionOrderingAndOwnership(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Uploads", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other uploads", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{8}, 32)
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, masterKey).Register(mux)

	first, second := []byte("first\n"), []byte("second\n")
	created := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", map[string]any{"bytes": len(first) + len(second), "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch", "expires_after": map[string]any{"anchor": "created_at", "seconds": 3600}})
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var upload openAIUpload
	if json.Unmarshal(created.Body.Bytes(), &upload) != nil || !strings.HasPrefix(upload.ID, "upload_") || upload.Object != "upload" || upload.Status != "pending" || upload.Bytes != int64(len(first)+len(second)) || upload.ExpiresAt-upload.CreatedAt != 3600 {
		t.Fatalf("upload=%s", created.Body.String())
	}
	if denied := performUploadPart(t, mux, otherSecret, upload.ID, first, nil); denied.Code != http.StatusNotFound {
		t.Fatalf("cross-key part status=%d body=%s", denied.Code, denied.Body.String())
	}
	partAResponse := performUploadPart(t, mux, secret, upload.ID, first, nil)
	partBResponse := performUploadPart(t, mux, secret, upload.ID, second, nil)
	if partAResponse.Code != http.StatusOK || partBResponse.Code != http.StatusOK {
		t.Fatalf("parts status=%d/%d bodies=%s / %s", partAResponse.Code, partBResponse.Code, partAResponse.Body.String(), partBResponse.Body.String())
	}
	var partA, partB openAIUploadPart
	_ = json.Unmarshal(partAResponse.Body.Bytes(), &partA)
	_ = json.Unmarshal(partBResponse.Body.Bytes(), &partB)
	if !strings.HasPrefix(partA.ID, "part_") || partA.Object != "upload.part" || partA.UploadID != upload.ID {
		t.Fatalf("part=%s", partAResponse.Body.String())
	}
	var ciphertext, nonce []byte
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT ciphertext,nonce FROM openai_upload_parts WHERE id=?`, partA.ID).Scan(&ciphertext, &nonce); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, first) || len(ciphertext) != len(first)+16 || len(nonce) != 12 {
		t.Fatalf("ciphertext=%d nonce=%d", len(ciphertext), len(nonce))
	}
	plain, err := credentials.Open(masterKey, ciphertext, nonce, openAIUploadPartAdditionalData(upload.ID, partA.ID, key.ID, int64(len(first))))
	if err != nil || !bytes.Equal(plain, first) {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	if denied := performUploadJSON(t, mux, otherSecret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", map[string]any{"part_ids": []string{partA.ID, partB.ID}}); denied.Code != http.StatusNotFound {
		t.Fatalf("cross-key complete status=%d body=%s", denied.Code, denied.Body.String())
	}
	if incomplete := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", map[string]any{"part_ids": []string{partA.ID}}); incomplete.Code != http.StatusBadRequest {
		t.Fatalf("incomplete status=%d body=%s", incomplete.Code, incomplete.Body.String())
	}
	completed := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", map[string]any{"part_ids": []string{partB.ID, partA.ID}})
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	if json.Unmarshal(completed.Body.Bytes(), &upload) != nil || upload.Status != "completed" || upload.File == nil || upload.File.Purpose != "batch" || upload.File.ExpiresAt-upload.File.CreatedAt != 3600 {
		t.Fatalf("completed=%s", completed.Body.String())
	}
	download := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+upload.File.ID+"/content", secret, nil, "")
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), append(append([]byte(nil), second...), first...)) {
		t.Fatalf("download status=%d body=%q", download.Code, download.Body.Bytes())
	}
	var remaining int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_upload_parts WHERE upload_id=?`, upload.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("parts=%d err=%v", remaining, err)
	}
	if again := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", map[string]any{"part_ids": []string{partA.ID}}); again.Code != http.StatusBadRequest {
		t.Fatalf("second complete status=%d body=%s", again.Code, again.Body.String())
	}
	if cancel := performFileRequest(t, mux, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/cancel", secret, nil, ""); cancel.Code != http.StatusBadRequest {
		t.Fatalf("completed cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
}

func TestOpenAIUploadCancellationAndExpiry(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Uploads", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{6}, 32)).Register(mux)
	create := func() openAIUpload {
		result := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", map[string]any{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"})
		var upload openAIUpload
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &upload) != nil {
			t.Fatalf("create status=%d body=%s", result.Code, result.Body.String())
		}
		return upload
	}

	cancelled := create()
	part := performUploadPart(t, mux, secret, cancelled.ID, []byte("{}"), nil)
	if part.Code != http.StatusOK {
		t.Fatalf("part status=%d body=%s", part.Code, part.Body.String())
	}
	result := performFileRequest(t, mux, http.MethodPost, "/api/openai/v1/uploads/"+cancelled.ID+"/cancel", secret, nil, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel status=%d body=%s", result.Code, result.Body.String())
	}
	if next := performUploadPart(t, mux, secret, cancelled.ID, []byte("{}"), nil); next.Code != http.StatusBadRequest {
		t.Fatalf("cancelled part status=%d body=%s", next.Code, next.Body.String())
	}

	expired := create()
	createdAt := time.Now().Add(-2 * time.Hour).UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_uploads SET created_at=?,expires_at=? WHERE id=?`, createdAt, createdAt+int64(openAIUploadLifetime/time.Millisecond), expired.ID); err != nil {
		t.Fatal(err)
	}
	if part := performUploadPart(t, mux, secret, expired.ID, []byte("{}"), nil); part.Code != http.StatusBadRequest {
		t.Fatalf("expired part status=%d body=%s", part.Code, part.Body.String())
	}
	if complete := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+expired.ID+"/complete", map[string]any{"part_ids": []string{"part_missing"}}); complete.Code != http.StatusBadRequest {
		t.Fatalf("expired complete status=%d body=%s", complete.Code, complete.Body.String())
	}
	if cancel := performFileRequest(t, mux, http.MethodPost, "/api/openai/v1/uploads/"+expired.ID+"/cancel", secret, nil, ""); cancel.Code != http.StatusBadRequest {
		t.Fatalf("expired cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
}

func TestOpenAIUploadValidationAndBounds(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Uploads", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No uploads", Scopes: []string{"chat:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{4}, 32)).Register(mux)
	valid := map[string]any{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"}
	if result := performUploadJSON(t, mux, deniedSecret, http.MethodPost, "/api/openai/v1/uploads", valid); result.Code != http.StatusForbidden {
		t.Fatalf("unscoped status=%d body=%s", result.Code, result.Body.String())
	}
	invalid := []map[string]any{
		{"bytes": 0, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"},
		{"bytes": maxFileBytes + 1, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"},
		{"bytes": 2, "filename": "../batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"},
		{"bytes": 2, "filename": "batch.txt", "mime_type": "application/jsonl", "purpose": "batch"},
		{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/json", "purpose": "batch"},
		{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "fine-tune"},
		{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch", "expires_after": map[string]any{"anchor": "created_at", "seconds": 3599}},
		{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch", "unknown": true},
	}
	for index, body := range invalid {
		if result := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", body); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid %d status=%d body=%s", index, result.Code, result.Body.String())
		}
	}

	uploadResult := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", map[string]any{"bytes": 17, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"})
	var upload openAIUpload
	if uploadResult.Code != http.StatusOK || json.Unmarshal(uploadResult.Body.Bytes(), &upload) != nil {
		t.Fatalf("create status=%d body=%s", uploadResult.Code, uploadResult.Body.String())
	}
	if empty := performUploadPart(t, mux, secret, upload.ID, nil, nil); empty.Code != http.StatusBadRequest {
		t.Fatalf("empty status=%d body=%s", empty.Code, empty.Body.String())
	}
	if extra := performUploadPart(t, mux, secret, upload.ID, []byte("x"), map[string][]byte{"other": []byte("x")}); extra.Code != http.StatusBadRequest {
		t.Fatalf("extra status=%d body=%s", extra.Code, extra.Body.String())
	}
	partIDs := make([]string, 0, maxOpenAIUploadParts)
	for index := 0; index < maxOpenAIUploadParts; index++ {
		part := performUploadPart(t, mux, secret, upload.ID, []byte("x"), nil)
		if part.Code != http.StatusOK {
			t.Fatalf("part %d status=%d body=%s", index, part.Code, part.Body.String())
		}
		var item openAIUploadPart
		if json.Unmarshal(part.Body.Bytes(), &item) != nil {
			t.Fatalf("part %d body=%s", index, part.Body.String())
		}
		partIDs = append(partIDs, item.ID)
	}
	if seventeenth := performUploadPart(t, mux, secret, upload.ID, []byte("x"), nil); seventeenth.Code != http.StatusBadRequest {
		t.Fatalf("part 17 status=%d body=%s", seventeenth.Code, seventeenth.Body.String())
	}
	if mismatch := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", map[string]any{"part_ids": partIDs}); mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatched bytes status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	largeUploadResult := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", map[string]any{"bytes": maxFileBytes, "filename": "large.jsonl", "mime_type": "application/jsonl", "purpose": "batch"})
	var largeUpload openAIUpload
	if largeUploadResult.Code != http.StatusOK || json.Unmarshal(largeUploadResult.Body.Bytes(), &largeUpload) != nil {
		t.Fatalf("large create status=%d body=%s", largeUploadResult.Code, largeUploadResult.Body.String())
	}
	if oversized := performUploadPart(t, mux, secret, largeUpload.ID, bytes.Repeat([]byte{'x'}, maxFileBytes+1), nil); oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", oversized.Code, oversized.Body.String())
	}
	for _, body := range []map[string]any{{"part_ids": []string{}}, {"part_ids": []string{"part_x", "part_x"}}, {"part_ids": []string{"part_x"}, "md5": "abc"}, {"part_ids": []string{"part_x"}, "other": true}} {
		if result := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads/"+upload.ID+"/complete", body); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid complete status=%d body=%s", result.Code, result.Body.String())
		}
	}
}

func TestOpenAIUploadCreationUsesRetainedCapacity(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Uploads", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for index := 0; index < retainedKeyJobs; index++ {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,0,zeroblob(16),zeroblob(12),?,?)`, "file_upload_capacity_"+strconv.Itoa(index), owner.ID, key.ID, "input.jsonl", "batch", now, now+int64(time.Hour/time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{2}, 32)).Register(mux)
	result := performUploadJSON(t, mux, secret, http.MethodPost, "/api/openai/v1/uploads", map[string]any{"bytes": 2, "filename": "batch.jsonl", "mime_type": "application/jsonl", "purpose": "batch"})
	if result.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
}

func performUploadJSON(t *testing.T, handler http.Handler, secret, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return performFileRequest(t, handler, method, path, secret, bytes.NewReader(body), "application/json")
}

func performUploadPart(t *testing.T, handler http.Handler, secret, uploadID string, content []byte, extras map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("data", "part.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	for name, value := range extras {
		extra, createErr := writer.CreateFormFile(name, name+".bin")
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, _ = extra.Write(value)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return performFileRequest(t, handler, http.MethodPost, "/api/openai/v1/uploads/"+uploadID+"/parts", secret, &body, writer.FormDataContentType())
}
