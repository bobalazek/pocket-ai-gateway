package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestBackgroundResponsesLifecycle(t *testing.T) {
	var calls atomic.Int64
	var leakedState atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if strings.Contains(string(body), `"background":true`) || strings.Contains(string(body), `"store":true`) {
			leakedState.Store(true)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"id":"upstream","object":"response","status":"completed","model":"upstream","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Background", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)

	cancelled := createBackground(t, mux, secret, `"cancel me"`)
	input := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+cancelled+"/input_items?limit=1", "")
	if input.Code != http.StatusOK || !strings.Contains(input.Body.String(), `"type":"input_text"`) {
		t.Fatalf("input items = %d %s", input.Code, input.Body.String())
	}
	cancel := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/"+cancelled+"/cancel", "")
	if cancel.Code != http.StatusOK || !strings.Contains(cancel.Body.String(), `"status":"cancelled"`) || calls.Load() != 0 {
		t.Fatalf("cancel = %d %s, calls=%d", cancel.Code, cancel.Body.String(), calls.Load())
	}

	completed := createBackground(t, mux, secret, `[{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]}]`)
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	waitResponseState(t, store.SystemDB(), completed, "completed")
	retrieved := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+completed, "")
	if retrieved.Code != http.StatusOK || !strings.Contains(retrieved.Body.String(), `"background":true`) || !strings.Contains(retrieved.Body.String(), `"model":"assistant"`) || calls.Load() != 1 || leakedState.Load() {
		t.Fatalf("completed = %d %s, calls=%d", retrieved.Code, retrieved.Body.String(), calls.Load())
	}
	listed := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+completed+"/input_items", "")
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(listed.Body.Bytes(), &page) != nil || listed.Code != http.StatusOK || len(page.Data) != 1 || page.Data[0]["type"] != "message" {
		t.Fatalf("input page = %d %s", listed.Code, listed.Body.String())
	}
	unsupported := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+completed+"/input_items?include%5B%5D=reasoning.encrypted_content", "")
	if unsupported.Code != http.StatusBadRequest || !strings.Contains(unsupported.Body.String(), "unsupported_feature") {
		t.Fatalf("include projection = %d %s", unsupported.Code, unsupported.Body.String())
	}
	var attempts int
	if err := store.SystemDB().QueryRow("SELECT COUNT(*) FROM attempts JOIN requests ON requests.id=attempts.request_id WHERE requests.key_id=?", key.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	deleted := performResponseRequest(t, mux, secret, http.MethodDelete, "/api/openai/v1/responses/"+completed, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestBackgroundResponseRevalidatesKeyAndRecoversInterruptedWork(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", "http://127.0.0.1:1/v1", "upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Background", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	id := createBackground(t, mux, secret, `"revoked"`)
	if err := keyService.Revoke(ctx, owner.ID, key.ID, key.Revision); err != nil {
		t.Fatal(err)
	}
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	waitResponseState(t, store.SystemDB(), id, "failed")
	stop()
	<-done
	var attempts int
	_ = store.SystemDB().QueryRow("SELECT COUNT(*) FROM attempts").Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("revoked job dispatched %d attempts", attempts)
	}

	now := time.Now()
	_, err = store.SystemDB().Exec(`INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json,lease_epoch,claimed_at) VALUES('resp_interrupted',?,?,?,?,?,?,'running',?,'old',?)`, owner.ID, key.ID, "assistant", responseState("resp_interrupted", "assistant", "in_progress", now, nil), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), []byte(`{"model":"assistant","input":"Hi","background":true}`), now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	restartContext, stopRestart := context.WithCancel(ctx)
	restarted := New(store.SystemDB(), keyService, providerService, usageService)
	restartDone := make(chan struct{})
	go func() { defer close(restartDone); restarted.RunBackground(restartContext) }()
	waitResponseState(t, store.SystemDB(), "resp_interrupted", "interrupted_unknown")
	stopRestart()
	<-restartDone
}

func TestInputItemsPagination(t *testing.T) {
	items, err := inputItems("resp_test", []byte(`{"input":[{"type":"message","role":"user","content":[]},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`))
	if err != nil || len(items) != 2 || !strings.Contains(string(items[0]), `"id":"item_`) || !strings.Contains(string(items[1]), `"call_id":"call_1"`) {
		t.Fatalf("items=%s err=%v", items, err)
	}
}

func TestInputItemsDefaultToDescendingOrderAndRejectLegacyRows(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", "http://127.0.0.1:1/v1", "upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Input", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := keyService.Authenticate(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	keyID := principal.KeyID
	now := time.Now()
	requestJSON := []byte(`{"model":"assistant","input":[{"type":"message","role":"user","content":[]},{"type":"function_call_output","call_id":"last","output":"ok"}]}`)
	for id, request := range map[string][]byte{"resp_order": requestJSON, "resp_legacy": nil} {
		_, err = store.SystemDB().Exec(`INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json) VALUES(?,?,?,?,?,?,?,?,?)`, id, owner.ID, keyID, "assistant", responseState(id, "assistant", "completed", now, nil), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), "completed", request)
		if err != nil {
			t.Fatal(err)
		}
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	ordered := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/resp_order/input_items?limit=1", "")
	if ordered.Code != http.StatusOK || !strings.Contains(ordered.Body.String(), `"call_id":"last"`) {
		t.Fatalf("default order = %d %s", ordered.Code, ordered.Body.String())
	}
	legacy := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/resp_legacy/input_items", "")
	if legacy.Code != http.StatusBadRequest || !strings.Contains(legacy.Body.String(), "before this gateway version") {
		t.Fatalf("legacy input = %d %s", legacy.Code, legacy.Body.String())
	}
}

func TestBackgroundQueueIsBoundedPerKey(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", "http://127.0.0.1:1/v1", "upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Bounded", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	for index := 0; index < backgroundKeyJobs; index++ {
		createBackground(t, mux, secret, `"queued"`)
	}
	response := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","input":"blocked","background":true}`)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("queue limit = %d %s", response.Code, response.Body.String())
	}
	principal, err := keyService.Authenticate(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index := backgroundKeyJobs; index < retainedKeyJobs; index++ {
		id := "resp_retained_" + strconv.Itoa(index)
		_, err = store.SystemDB().Exec(`INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json) VALUES(?,?,?,?,?,?,?,?,?)`, id, owner.ID, principal.KeyID, "assistant", responseState(id, "assistant", "cancelled", now, nil), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), "cancelled", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRetainedResponseCapacity(ctx, store.SystemDB(), owner.ID, principal.KeyID, 1); !errors.Is(err, errStoredResponseLimit) {
		t.Fatalf("retained limit error = %v", err)
	}
}

func TestBackgroundCompletionWaitsForDurableSettlement(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"id":"upstream","object":"response","status":"completed","model":"upstream","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Settlement", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().Exec(`CREATE TRIGGER block_response_settlement BEFORE UPDATE OF state ON attempts WHEN NEW.state='succeeded' BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	id := createBackground(t, mux, secret, `"settle"`)
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var attempts int
		var state string
		_ = store.SystemDB().QueryRow("SELECT COUNT(*) FROM attempts").Scan(&attempts)
		_ = store.SystemDB().QueryRow("SELECT state FROM stored_responses WHERE id=?", id).Scan(&state)
		if attempts == 1 && state == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var state string
	_ = store.SystemDB().QueryRow("SELECT state FROM stored_responses WHERE id=?", id).Scan(&state)
	if state != "running" {
		t.Fatalf("response completed before settlement: %s", state)
	}
	if _, err := store.SystemDB().Exec(`DROP TRIGGER block_response_settlement`); err != nil {
		t.Fatal(err)
	}
	waitResponseState(t, store.SystemDB(), id, "completed")
	stop()
	<-done
}

func TestResponseCompactionUsesOnlyNativeCapableTargets(t *testing.T) {
	var path, model string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		model, _ = body["model"].(string)
		_, _ = io.WriteString(response, `{"id":"resp_compact","object":"response.compaction","created_at":1,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	openAIConnection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "private-openai", []string{"chat"})
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset='openai' WHERE id=?", openAIConnection.ID); err != nil {
		t.Fatal(err)
	}
	anthropicConnection, anthropicModel := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "private-anthropic", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Compact", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"*"}, ConnectionIDs: []string{openAIConnection.ID, anthropicConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	compacted := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/compact", `{"model":"assistant","input":"Long conversation"}`)
	if compacted.Code != http.StatusOK || !strings.Contains(compacted.Body.String(), `"object":"response.compaction"`) || path != "/v1/responses/compact" || model != "private-openai" {
		t.Fatalf("compact = %d %s path=%s model=%s", compacted.Code, compacted.Body.String(), path, model)
	}
	var dialect string
	if err := store.SystemDB().QueryRow("SELECT dialect FROM requests ORDER BY started_at DESC LIMIT 1").Scan(&dialect); err != nil || dialect != "responses" {
		t.Fatalf("stored dialect = %q, %v", dialect, err)
	}
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "output_tokens", Algorithm: "ceiling", LimitUnits: 1024}); err != nil {
		t.Fatal(err)
	}
	bounded := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/compact", `{"model":"assistant","input":"Bound this compaction"}`)
	if bounded.Code != http.StatusOK {
		t.Fatalf("bounded compact = %d %s", bounded.Code, bounded.Body.String())
	}
	for _, body := range []string{`{"model":"assistant","input":null}`, `{"model":"assistant","input":[null]}`, `{"model":"assistant","input":[{"type":"item_reference","id":"item_1"}]}`, `{"model":"assistant","input":[{"type":"input_file","file_id":"file_1"}]}`, `{"model":"assistant","input":"Long conversation","stream":true}`, `{"model":"assistant","input":"Long conversation","stream":null}`, `{"model":"assistant","input":"Long conversation","stream":""}`} {
		path = ""
		invalid := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/compact", body)
		if invalid.Code != http.StatusBadRequest || path != "" {
			t.Fatalf("invalid compact = %d %s path=%s", invalid.Code, invalid.Body.String(), path)
		}
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset='custom' WHERE id=?", openAIConnection.ID); err != nil {
		t.Fatal(err)
	}
	path = ""
	custom := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/compact", `{"model":"assistant","input":"Long conversation"}`)
	if custom.Code != http.StatusNotFound || path != "" {
		t.Fatalf("custom compact = %d %s path=%s", custom.Code, custom.Body.String(), path)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset='openai' WHERE id=?", openAIConnection.ID); err != nil {
		t.Fatal(err)
	}
	path = ""
	unsupported := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses/compact", `{"model":"`+anthropicModel.ID+`","input":"Long conversation"}`)
	if unsupported.Code != http.StatusNotFound || path != "" {
		t.Fatalf("translated compact = %d %s path=%s", unsupported.Code, unsupported.Body.String(), path)
	}
}

func createBackground(t *testing.T, handler http.Handler, secret, input string) string {
	t.Helper()
	response := performResponseRequest(t, handler, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","input":`+input+`,"background":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var value struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.ID == "" || value.Status != "queued" {
		t.Fatalf("queued response = %s, %v", response.Body.String(), err)
	}
	return value.ID
}

func performResponseRequest(t *testing.T, handler http.Handler, secret, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitResponseState(t *testing.T, database interface{ QueryRow(string, ...any) *sql.Row }, id, wanted string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if database.QueryRow("SELECT state FROM stored_responses WHERE id=?", id).Scan(&state) == nil && state == wanted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("response %s did not reach %s", id, wanted)
}
