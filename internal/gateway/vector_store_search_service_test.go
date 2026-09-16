package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestSearchVectorStoresRanksGloballyAndTouchesEachStore(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Search", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other search", Scopes: []string{"vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{9}, 32))
	mux := http.NewServeMux()
	handler.Register(mux)

	createStore := func(secret, name string) vectorStore {
		result := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"`+name+`"}`)
		var item vectorStore
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &item) != nil {
			t.Fatalf("create store status=%d body=%s", result.Code, result.Body.String())
		}
		return item
	}
	attach := func(storeID, filename, content string) openAIFile {
		result := performFileUpload(t, mux, secret, filename, []byte(content), map[string]string{"purpose": "user_data"}, nil)
		var file openAIFile
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &file) != nil {
			t.Fatalf("upload status=%d body=%s", result.Code, result.Body.String())
		}
		result = performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeID+"/files", secret, `{"file_id":"`+file.ID+`"}`)
		if result.Code != http.StatusOK {
			t.Fatalf("attach status=%d body=%s", result.Code, result.Body.String())
		}
		return file
	}

	first, second := createStore(secret, "First"), createStore(secret, "Second")
	foreign := createStore(otherSecret, "Foreign")
	attach(first.ID, "first.txt", "gateway notes")
	winner := attach(second.ID, "second.txt", "gateway gateway")
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_vector_stores SET created_at=0,last_active_at=0 WHERE id IN (?,?)`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	options, err := parseVectorStoreSearch([]byte(`{"query":"gateway","max_num_results":1}`))
	if err != nil {
		t.Fatal(err)
	}
	results, err := handler.searchVectorStores(ctx, key.ID, []string{first.ID, second.ID, first.ID}, options)
	if err != nil || len(results) != 1 || results[0].FileID != winner.ID {
		t.Fatalf("results=%#v error=%v", results, err)
	}
	for _, id := range []string{first.ID, second.ID} {
		var lastActive int64
		if err := store.SystemDB().QueryRowContext(ctx, `SELECT last_active_at FROM openai_vector_stores WHERE id=?`, id).Scan(&lastActive); err != nil || lastActive == 0 {
			t.Fatalf("store %s last_active_at=%d error=%v", id, lastActive, err)
		}
	}

	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_vector_stores SET last_active_at=0 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.searchVectorStores(ctx, key.ID, []string{first.ID, foreign.ID}, options); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign store error=%v", err)
	}
	var lastActive int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT last_active_at FROM openai_vector_stores WHERE id=?`, first.ID).Scan(&lastActive); err != nil || lastActive != 0 {
		t.Fatalf("failed search touched store: last_active_at=%d error=%v", lastActive, err)
	}

	handler.fileTransfers <- struct{}{}
	defer handler.releaseFileTransfer()
	if _, err := handler.searchVectorStores(ctx, key.ID, []string{first.ID}, options); !errors.Is(err, errVectorStoreSearchBusy) {
		t.Fatalf("busy transfer error=%v", err)
	}
}

func TestVectorStoreSearchErrorClassification(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{sql.ErrNoRows, http.StatusNotFound, "not_found"},
		{errVectorStoreSearchBusy, http.StatusTooManyRequests, "rate_limit_exceeded"},
		{fmt.Errorf("%w: invalid content", errVectorStoreSearchContent), http.StatusBadRequest, "unsupported_feature"},
		{errors.New("storage unavailable"), http.StatusServiceUnavailable, "gateway_unavailable"},
	}
	for _, test := range tests {
		status, code, _ := vectorStoreSearchError(test.err)
		if status != test.status || code != test.code {
			t.Fatalf("error=%v status=%d code=%q", test.err, status, code)
		}
	}
}
