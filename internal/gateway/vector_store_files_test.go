package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestOpenAIVectorStoreFileLifecycle(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Store files", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Denied", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{8}, 32)).Register(mux)
	uploaded := performFileUpload(t, mux, secret, "notes.txt", []byte("gateway notes"), map[string]string{"purpose": "user_data"}, nil)
	var file openAIFile
	if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Knowledge"}`)
	var storeItem vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &storeItem) != nil {
		t.Fatalf("create store status=%d body=%s", created.Code, created.Body.String())
	}
	path := "/api/openai/v1/vector_stores/" + storeItem.ID + "/files"
	attached := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"`+file.ID+`","attributes":{"kind":"docs","priority":2,"active":true},"chunking_strategy":{"type":"auto"}}`)
	var item vectorStoreFile
	if attached.Code != http.StatusOK || json.Unmarshal(attached.Body.Bytes(), &item) != nil || item.ID != file.ID || item.VectorStoreID != storeItem.ID || item.Status != "completed" || item.UsageBytes != file.Bytes || item.Attributes["kind"] != "docs" || item.ChunkingStrategy["type"] != "other" {
		t.Fatalf("attach status=%d body=%s", attached.Code, attached.Body.String())
	}
	if duplicate := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"`+file.ID+`"}`); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodPost, path, otherSecret, `{"file_id":"`+file.ID+`"}`); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	if denied := performVectorStoreRequest(t, mux, http.MethodGet, path, deniedSecret, ""); denied.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", denied.Code, denied.Body.String())
	}
	if static := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"missing","chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`); static.Code != http.StatusBadRequest || !strings.Contains(static.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("static status=%d body=%s", static.Code, static.Body.String())
	}
	if invalid := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"missing","attributes":{"nested":{}}}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	listed := performVectorStoreRequest(t, mux, http.MethodGet, path+"?filter=completed&limit=1", secret, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), file.ID) || !strings.Contains(listed.Body.String(), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	resource := path + "/" + file.ID
	content := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/content", secret, "")
	if content.Code != http.StatusOK || content.Header().Get("Cache-Control") != "no-store" || content.Body.String() != "{\"data\":[{\"type\":\"text\",\"text\":\"gateway notes\"}],\"object\":\"list\"}\n" {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/content", otherSecret, ""); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign content status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	updated := performVectorStoreRequest(t, mux, http.MethodPost, resource, secret, `{"attributes":{"kind":"updated"}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"kind":"updated"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	searched := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", secret, `{"query":"gateway notes","filters":{"type":"eq","key":"kind","value":"updated"},"max_num_results":5,"ranking_options":{"ranker":"none","score_threshold":0.1}}`)
	var searchPage struct {
		Object string                    `json:"object"`
		Data   []vectorStoreSearchResult `json:"data"`
	}
	if searched.Code != http.StatusOK || json.Unmarshal(searched.Body.Bytes(), &searchPage) != nil || searchPage.Object != "vector_store.search_results.page" || len(searchPage.Data) != 1 || searchPage.Data[0].FileID != file.ID || searchPage.Data[0].Score <= 0 || searchPage.Data[0].Content[0].Text != "gateway notes" {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", otherSecret, `{"query":"gateway"}`); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign search status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	if rewritten := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", secret, `{"query":"gateway","rewrite_query":true}`); rewritten.Code != http.StatusBadRequest || !strings.Contains(rewritten.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("rewrite status=%d body=%s", rewritten.Code, rewritten.Body.String())
	}
	storeResult := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+storeItem.ID, secret, "")
	var aggregate vectorStore
	if storeResult.Code != http.StatusOK || json.Unmarshal(storeResult.Body.Bytes(), &aggregate) != nil || aggregate.UsageBytes != file.Bytes || aggregate.FileCounts.Completed != 1 || aggregate.FileCounts.Total != 1 {
		t.Fatalf("aggregate status=%d body=%s", storeResult.Code, storeResult.Body.String())
	}
	deleted := performVectorStoreRequest(t, mux, http.MethodDelete, resource, secret, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"object":"vector_store.file.deleted"`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if missing := performVectorStoreRequest(t, mux, http.MethodGet, resource, secret, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	if retained := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID, secret, nil, ""); retained.Code != http.StatusOK {
		t.Fatalf("source file status=%d body=%s", retained.Code, retained.Body.String())
	}
}

func TestVectorStoreTextChunksPreserveUTF8WithinBounds(t *testing.T) {
	input := []byte(strings.Repeat("a", maxVectorStoreContentChunkBytes-1) + "🙂" + strings.Repeat("b", maxVectorStoreContentChunkBytes))
	chunks, err := vectorStoreTextChunks(input)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("chunks=%d error=%v", len(chunks), err)
	}
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		if len(chunk.Text) > maxVectorStoreContentChunkBytes || !utf8.ValidString(chunk.Text) {
			t.Fatalf("invalid chunk size=%d", len(chunk.Text))
		}
		rebuilt.WriteString(chunk.Text)
	}
	if rebuilt.String() != string(input) {
		t.Fatal("chunks did not preserve source text")
	}
	if _, err := vectorStoreTextChunks([]byte{0xff}); err == nil {
		t.Fatal("binary content was accepted")
	}
}
