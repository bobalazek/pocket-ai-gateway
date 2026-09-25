package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestStoredChatCompletionLifecycleAndKeyIsolation(t *testing.T) {
	var upstreamBody map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"provider-id","object":"chat.completion","created":100,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored chat", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other key", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(method, path, key, body string) (int, []byte) {
		request, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		result, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer result.Body.Close()
		encoded, _ := io.ReadAll(result.Body)
		return result.StatusCode, encoded
	}
	status, body := call(http.MethodPost, "/api/openai/v1/chat/completions", secret, `{"model":"`+model.ID+`","store":true,"metadata":{"topic":"café"},"messages":[{"role":"system","content":"Concise"},{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`)
	if status != http.StatusOK {
		t.Fatalf("create status=%d body=%s", status, body)
	}
	var created map[string]json.RawMessage
	_ = json.Unmarshal(body, &created)
	var id, gotModel string
	_ = json.Unmarshal(created["id"], &id)
	_ = json.Unmarshal(created["model"], &gotModel)
	if !strings.HasPrefix(id, "chatcmpl_") || id == "provider-id" || gotModel != model.ID || string(created["metadata"]) != `{"topic":"café"}` {
		t.Fatalf("created=%s", body)
	}
	if string(upstreamBody["store"]) != "false" || upstreamBody["metadata"] != nil {
		t.Fatalf("upstream stored request=%s", mustMarshalTest(upstreamBody))
	}
	if status, _ = call(http.MethodGet, "/api/openai/v1/chat/completions/"+id, otherSecret, ""); status != http.StatusNotFound {
		t.Fatalf("cross-key retrieve status=%d", status)
	}
	status, body = call(http.MethodGet, "/api/openai/v1/chat/completions?model="+model.ID+"&metadata%5Btopic%5D=caf%C3%A9&limit=1", secret, "")
	if status != http.StatusOK || !strings.Contains(string(body), id) || !strings.Contains(string(body), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", status, body)
	}
	status, body = call(http.MethodGet, "/api/openai/v1/chat/completions/"+id+"/messages?limit=1", secret, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"role":"system"`) || !strings.Contains(string(body), `"has_more":true`) {
		t.Fatalf("messages status=%d body=%s", status, body)
	}
	var page struct {
		LastID string `json:"last_id"`
	}
	_ = json.Unmarshal(body, &page)
	status, body = call(http.MethodGet, "/api/openai/v1/chat/completions/"+id+"/messages?after="+page.LastID, secret, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"role":"user"`) || !strings.Contains(string(body), `"content":null`) || !strings.Contains(string(body), `"content_parts":[`) {
		t.Fatalf("second message page status=%d body=%s", status, body)
	}
	status, body = call(http.MethodPost, "/api/openai/v1/chat/completions/"+id, secret, `{"metadata":null}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"metadata":null`) {
		t.Fatalf("update status=%d body=%s", status, body)
	}
	status, body = call(http.MethodPost, "/api/openai/v1/chat/completions/"+id, secret, `{"metadata":{"topic":"tea"}}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"topic":"tea"`) {
		t.Fatalf("second update status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, "/api/openai/v1/chat/completions?metadata%5Btopic%5D=caf%C3%A9", secret, ""); status != http.StatusOK || strings.Contains(string(body), id) {
		t.Fatalf("stale metadata filter status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, "/api/openai/v1/chat/completions?metadata%5Btopic%5D=tea", secret, ""); status != http.StatusOK || !strings.Contains(string(body), id) {
		t.Fatalf("updated metadata filter status=%d body=%s", status, body)
	}
	status, body = call(http.MethodDelete, "/api/openai/v1/chat/completions/"+id, secret, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"object":"chat.completion.deleted"`) {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	if status, _ = call(http.MethodGet, "/api/openai/v1/chat/completions/"+id, secret, ""); status != http.StatusNotFound {
		t.Fatalf("retrieve after delete status=%d", status)
	}
}

func TestStoredChatCompletionValidationPrecedesDispatch(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		io.WriteString(w, `{"object":"chat.completion","choices":[]}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored chat", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","store":null,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("store:null status=%d body=%s", response.Code, response.Body.String())
	}
	var stored int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM stored_chat_completions`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored rows=%d err=%v", stored, err)
	}
	for _, body := range []string{
		`{"model":"` + model.ID + `","store":true,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
		`{"model":"` + model.ID + `","store":true,"metadata":{"x":1},"messages":[{"role":"user","content":"Hi"}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[null]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":42}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":{}}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[null]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[{"type":"text"}]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[{"type":"text","text":1}]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{}}]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":""}}]}]}`,
		`{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"eA==","format":"wav"}}]}]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+secret)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("payload=%s status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestStoredChatMessageContentUnionAndAbsentContentNormalization(t *testing.T) {
	messages, err := validateStoredChatMessages(json.RawMessage(`[
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function"}]},
		{"role":"assistant","content":null},
		{"role":"user","content":"hello"},
		{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,eA=="}}]}
	]`))
	if err != nil || !strings.Contains(string(messages), `"content":null`) {
		t.Fatalf("normalized messages=%s err=%v", messages, err)
	}
	request := append([]byte(`{"messages":`), messages...)
	request = append(request, '}')
	listed, err := storedChatMessages("chatcmpl_test", request)
	if err != nil || len(listed) != 4 || !strings.Contains(string(listed[0]), `"content":null`) || !strings.Contains(string(listed[3]), `"content_parts":[`) {
		t.Fatalf("listed messages=%s err=%v", listed, err)
	}
}

func TestStoredPublicationRequiresDurableSettlement(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/chat/completions") {
			io.WriteString(response, `{"id":"upstream","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		io.WriteString(response, `{"id":"upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Settlement gate", Scopes: []string{"chat:generate", "responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().Exec(`CREATE TRIGGER block_sync_settlement BEFORE UPDATE OF state ON attempts WHEN NEW.state='succeeded' BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	requests := []struct {
		path, body, table string
	}{
		{"/api/openai/v1/chat/completions", `{"model":"` + model.ID + `","store":true,"messages":[{"role":"user","content":"Hi"}]}`, "stored_chat_completions"},
		{"/api/openai/v1/responses", `{"model":"` + model.ID + `","input":"Hi"}`, "stored_responses"},
	}
	for _, test := range requests {
		result := performResponseRequest(t, mux, secret, http.MethodPost, test.path, test.body)
		if result.Code != http.StatusServiceUnavailable || strings.Contains(result.Body.String(), `"object":"chat.completion"`) || strings.Contains(result.Body.String(), `"object":"response"`) {
			t.Fatalf("%s status=%d body=%s", test.path, result.Code, result.Body.String())
		}
		var stored int
		if err := store.SystemDB().QueryRow(`SELECT COUNT(*) FROM ` + test.table).Scan(&stored); err != nil || stored != 0 {
			t.Fatalf("%s rows=%d err=%v", test.table, stored, err)
		}
	}
	var succeeded int
	if err := store.SystemDB().QueryRow(`SELECT COUNT(*) FROM requests WHERE state='succeeded'`).Scan(&succeeded); err != nil || succeeded != 0 {
		t.Fatalf("succeeded requests=%d err=%v", succeeded, err)
	}
}

func TestStoredChatCompletionMalformedSuccessDoesNotFallback(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls++
		io.WriteString(w, `{"object":"wrong","choices":[]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls++
		io.WriteString(w, `{"object":"chat.completion","choices":[]}`)
	}))
	defer second.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", first.URL+"/v1", "provider-one", []string{"chat"})
	secondConnection, err := providerService.CreateConnection(ctx, owner, keysConnectionInput("second", second.URL+"/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "provider-two", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored chat", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","store":true,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("status=%d calls=%d/%d body=%s", response.Code, firstCalls, secondCalls, response.Body.String())
	}
}

func TestStoredChatCompletionStorageFailurePreservesProviderUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"provider-id","object":"chat.completion","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored chat", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_stored_chat BEFORE INSERT ON stored_chat_completions BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","store":true,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var attemptState, requestState string
	var input, output int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,input_tokens,output_tokens FROM attempts ORDER BY started_at DESC LIMIT 1`).Scan(&attemptState, &input, &output); err != nil || attemptState != "succeeded" || input != 4 || output != 2 {
		t.Fatalf("attempt=%s %d/%d err=%v", attemptState, input, output, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests ORDER BY started_at DESC LIMIT 1`).Scan(&requestState); err != nil || requestState != "failed" {
		t.Fatalf("request=%s err=%v", requestState, err)
	}
}

func TestFailedStoredPublicationIsRepairedAfterImmediateCleanupFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"provider-id","object":"chat.completion","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Repair", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `
		CREATE TRIGGER block_repair_store BEFORE INSERT ON stored_chat_completions BEGIN SELECT RAISE(ABORT,'blocked'); END;
		CREATE TRIGGER block_repair_finalize BEFORE UPDATE OF state ON requests WHEN NEW.state='failed' BEGIN SELECT RAISE(ABORT,'blocked'); END;
	`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","store":true,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var requestID, state string
	var leases int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT id,state FROM requests ORDER BY started_at DESC LIMIT 1`).Scan(&requestID, &state); err != nil || state != "in_progress" {
		t.Fatalf("request=%s state=%s err=%v", requestID, state, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID).Scan(&leases); err != nil || leases != 1 {
		t.Fatalf("request leases=%d err=%v", leases, err)
	}
	if repaired, err := usageService.RepairStaleRequests(ctx); err != nil || repaired != 0 {
		t.Fatalf("fresh repaired=%d err=%v", repaired, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DROP TRIGGER block_repair_finalize`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE requests SET started_at=? WHERE id=?; UPDATE attempts SET finished_at=? WHERE request_id=?`, old, requestID, old, requestID); err != nil {
		t.Fatal(err)
	}
	repaired, err := usageService.RepairStaleRequests(ctx)
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, requestID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("repaired request state=%s err=%v", state, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("repaired request leases=%d err=%v", leases, err)
	}
}

func TestStoredChatCompletionListUsesFilteredKeysetAndByteBound(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "List", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	insert := func(id, model string, created int64, metadata json.RawMessage, body []byte) {
		t.Helper()
		tx, err := store.SystemDB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `INSERT INTO stored_chat_completions(id,owner_user_id,key_id,model_id,body_json,request_json,metadata_json,created_at,expires_at) VALUES(?,?,?,?,?,'{}',?,?,?)`, id, owner.ID, key.ID, model, body, metadata, created, now.Add(time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if err = replaceStoredChatMetadata(ctx, tx, id, metadata); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	object := func(id string) []byte { return []byte(`{"id":"` + id + `","object":"chat.completion"}`) }
	insert("chatcmpl_a", "assistant", 1, json.RawMessage(`{"topic":"x"}`), object("chatcmpl_a"))
	insert("chatcmpl_b", "other", 2, json.RawMessage(`{"topic":"x"}`), object("chatcmpl_b"))
	insert("chatcmpl_c", "assistant", 3, json.RawMessage(`{"topic":"x"}`), object("chatcmpl_c"))
	insert("chatcmpl_d", "assistant", 4, json.RawMessage(`{"topic":"y"}`), object("chatcmpl_d"))
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	first := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/chat/completions?model=assistant&metadata%5Btopic%5D=x&limit=1", "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"chatcmpl_a"`) || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("first page=%d %s", first.Code, first.Body.String())
	}
	second := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/chat/completions?model=assistant&metadata%5Btopic%5D=x&limit=1&after=chatcmpl_a", "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"chatcmpl_c"`) || strings.Contains(second.Body.String(), `"chatcmpl_b"`) || !strings.Contains(second.Body.String(), `"has_more":false`) {
		t.Fatalf("second page=%d %s", second.Code, second.Body.String())
	}
	descending := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/chat/completions?model=assistant&metadata%5Btopic%5D=x&limit=1&order=desc&after=chatcmpl_c", "")
	if descending.Code != http.StatusOK || !strings.Contains(descending.Body.String(), `"chatcmpl_a"`) {
		t.Fatalf("descending page=%d %s", descending.Code, descending.Body.String())
	}

	largeBody := func(id, fill string) []byte {
		return []byte(`{"id":"` + id + `","object":"chat.completion","padding":"` + fill + `"}`)
	}
	padding := strings.Repeat("x", 9<<20)
	insert("chatcmpl_e", "large", 5, json.RawMessage(`{}`), largeBody("chatcmpl_e", padding))
	insert("chatcmpl_f", "large", 6, json.RawMessage(`{}`), largeBody("chatcmpl_f", padding))
	bounded := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/chat/completions?model=large&limit=100", "")
	if bounded.Code != http.StatusOK || !strings.Contains(bounded.Body.String(), `"chatcmpl_e"`) || strings.Contains(bounded.Body.String(), `"chatcmpl_f"`) || !strings.Contains(bounded.Body.String(), `"has_more":true`) || bounded.Body.Len() > 10<<20 {
		t.Fatalf("bounded page status=%d bytes=%d", bounded.Code, bounded.Body.Len())
	}
	continued := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/chat/completions?model=large&limit=100&after=chatcmpl_e", "")
	if continued.Code != http.StatusOK || !strings.Contains(continued.Body.String(), `"chatcmpl_f"`) {
		t.Fatalf("continued page status=%d bytes=%d", continued.Code, continued.Body.Len())
	}
}

func TestStoredChatMetadataGrowthCountsBodyAndColumn(t *testing.T) {
	oldBody, oldMetadata := []byte(`{"metadata":{}}`), []byte(`{}`)
	newBody, newMetadata := []byte(`{"metadata":{"a":"b"}}`), []byte(`{"a":"b"}`)
	want := int64(len(newBody) + len(newMetadata) - len(oldBody) - len(oldMetadata))
	got := storedChatMetadataGrowth(int64(len(oldBody)), int64(len(oldMetadata)), newBody, newMetadata)
	if got != want || got <= int64(len(newMetadata)-len(oldMetadata)) {
		t.Fatalf("growth=%d want=%d metadata-only=%d", got, want, len(newMetadata)-len(oldMetadata))
	}
}

func TestStoredChatCompletionsShareResponseRetentionCeilings(t *testing.T) {
	ctx, store, owner, keyService, _, _ := gatewayFixture(t)
	defer store.Close()
	key, _, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index := 0; index < retainedKeyJobs; index++ {
		_, err = store.SystemDB().ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,request_json,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, "resp_"+strconv.Itoa(index), owner.ID, key.ID, "assistant", []byte(`{}`), []byte(`{}`), now.UnixMilli(), now.Add(time.Hour).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, key.ID, 1, 1); !errors.Is(err, errRetainedResourceLimit) {
		t.Fatalf("capacity error=%v", err)
	}
}

func TestExpiredStoredChatCompletionIsHidden(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "provider-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored chat", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var keyID string
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT id FROM api_keys WHERE label='Stored chat'`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	_, err = store.SystemDB().ExecContext(ctx, `INSERT INTO stored_chat_completions(id,owner_user_id,key_id,model_id,body_json,request_json,metadata_json,created_at,expires_at) VALUES('chatcmpl_expired',?,?, 'assistant','{}','{}','{}',?,?)`, owner.ID, keyID, time.Now().Add(-time.Hour).UnixMilli(), time.Now().Add(-time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/api/openai/v1/chat/completions/chatcmpl_expired", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func mustMarshalTest(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func keysConnectionInput(name, baseURL string) providers.ConnectionInput {
	return providers.ConnectionInput{Name: name, Adapter: "openai", BaseURL: baseURL, Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000}
}

func TestNativeChatStreamRequestsUsageForAccounting(t *testing.T) {
	var upstreamOptions []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StreamOptions json.RawMessage `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamOptions = append(upstreamOptions, string(body.StreamOptions))
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"stop\"}],\"usage\":null}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "up-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "stream", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, test := range []struct {
		options   string
		wantUsage bool
	}{{"", false}, {`,"stream_options":{"include_usage":true}`, true}} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"Hi"}]`+test.options+`}`))
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if got := strings.Contains(string(body), `"prompt_tokens"`); got != test.wantUsage || !strings.Contains(string(body), "[DONE]") {
			t.Fatalf("options %q: client body = %s", test.options, body)
		}
		requestID := response.Header.Get(pocketAIRequestIDHeader)
		var status string
		var input, output int64
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens FROM attempts WHERE request_id=?", requestID).Scan(&status, &input, &output) == nil && status != "" {
				break
			}
		}
		if status != "provider_reported" || input != 3 || output != 1 {
			t.Fatalf("options %q: usage = %s %d/%d", test.options, status, input, output)
		}
	}
	for _, options := range upstreamOptions {
		if options != `{"include_usage":true}` {
			t.Fatalf("upstream stream_options = %s", options)
		}
	}
}

func TestChatStreamObserverRequiresCleanDone(t *testing.T) {
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\n"
	for source, wantErr := range map[string]bool{
		chunk + "data: [DONE]\n\n": false,
		chunk + "data:[DONE]":      false,
		chunk:                      true,
		chunk + "data: {\"id\":\"x\",\"error\":{\"message\":\"overloaded\"},\"choices\":[]}\n\ndata: [DONE]\n\n": true,
		"data: {\"choices\":[{\"delta\":{\"content\":\"\\\"error\\\"\"}}],\"error\":null}\n\ndata: [DONE]\n\n":   false,
	} {
		observer := &chatStreamObserver{}
		for index := range source {
			_, _ = observer.Write([]byte{source[index]})
		}
		if err := observer.result(); (err != nil) != wantErr || err != nil && !errors.Is(err, errUpstreamResponseInterrupted) {
			t.Fatalf("source %q: error = %v", source, err)
		}
	}
}
