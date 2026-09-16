package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestOpenAIVectorStoreFileBatchLifecycleAndAtomicity(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Store batches", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other batches", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{9}, 32)).Register(mux)
	upload := func(name, content string) openAIFile {
		result := performFileUpload(t, mux, secret, name, []byte(content), map[string]string{"purpose": "user_data"}, nil)
		var file openAIFile
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &file) != nil {
			t.Fatalf("upload %s status=%d body=%s", name, result.Code, result.Body.String())
		}
		return file
	}
	first, second := upload("first.txt", "first"), upload("second.txt", "second")
	createdStore := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Batched"}`)
	var vectorStore vectorStore
	if createdStore.Code != http.StatusOK || json.Unmarshal(createdStore.Body.Bytes(), &vectorStore) != nil {
		t.Fatalf("create store status=%d body=%s", createdStore.Code, createdStore.Body.String())
	}
	path := "/api/openai/v1/vector_stores/" + vectorStore.ID + "/file_batches"
	failed := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_ids":["`+first.ID+`","file_missing"]}`)
	if failed.Code != http.StatusNotFound {
		t.Fatalf("atomic failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	var attachments, batches int
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_store_files WHERE vector_store_id=?`, vectorStore.ID).Scan(&attachments); err != nil || attachments != 0 {
		t.Fatalf("attachments=%d error=%v", attachments, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_store_file_batches WHERE vector_store_id=?`, vectorStore.ID).Scan(&batches); err != nil || batches != 0 {
		t.Fatalf("batches=%d error=%v", batches, err)
	}

	created := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"files":[{"file_id":"`+first.ID+`","attributes":{"kind":"first"}},{"file_id":"`+second.ID+`","attributes":{"kind":"second"},"chunking_strategy":{"type":"auto"}}]}`)
	var batch vectorStoreFileBatch
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &batch) != nil || !strings.HasPrefix(batch.ID, "vsfb_") || batch.Object != "vector_store.files_batch" || batch.Status != "completed" || batch.FileCounts.Completed != 2 || batch.FileCounts.Total != 2 {
		t.Fatalf("create batch status=%d body=%s", created.Code, created.Body.String())
	}
	resource := path + "/" + batch.ID
	if retrieved := performVectorStoreRequest(t, mux, http.MethodGet, resource, secret, ""); retrieved.Code != http.StatusOK || retrieved.Body.String() != created.Body.String() {
		t.Fatalf("retrieve status=%d body=%s", retrieved.Code, retrieved.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodGet, resource, otherSecret, ""); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	listed := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/files?order=asc&limit=1", secret, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"object":"vector_store.file"`) || !strings.Contains(listed.Body.String(), `"has_more":true`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	failedOnly := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/files?filter=failed", secret, "")
	if failedOnly.Code != http.StatusOK || !strings.Contains(failedOnly.Body.String(), `"data":[]`) {
		t.Fatalf("filtered status=%d body=%s", failedOnly.Code, failedOnly.Body.String())
	}
	cancelled := performVectorStoreRequest(t, mux, http.MethodPost, resource+"/cancel", secret, `{}`)
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"completed"`) {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	duplicate := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_ids":["`+first.ID+`","`+first.ID+`"]}`)
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestOpenAIVectorStoreCreateWithFilesIsAtomic(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Store create files", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{7}, 32)).Register(mux)
	uploaded := performFileUpload(t, mux, secret, "created-with-store.txt", []byte("store content"), map[string]string{"purpose": "user_data"}, nil)
	var file openAIFile
	if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Ready","file_ids":["`+file.ID+`"],"chunking_strategy":{"type":"auto"}}`)
	var item vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &item) != nil || item.FileCounts.Completed != 1 || item.FileCounts.Total != 1 || item.UsageBytes != file.Bytes {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	failed := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Rollback","file_ids":["`+file.ID+`","file_missing"]}`)
	if failed.Code != http.StatusNotFound {
		t.Fatalf("failed status=%d body=%s", failed.Code, failed.Body.String())
	}
	var count int
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_stores WHERE name='Rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback stores=%d error=%v", count, err)
	}
}
