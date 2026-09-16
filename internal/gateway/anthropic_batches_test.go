package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestMessageBatchExecutesLocallyAndReturnsJSONL(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/v1/messages" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		if request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", request.Header.Get("anthropic-version"))
		}
		io.WriteString(response, `{"id":"msg_upstream","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batch", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService, "https://gateway.example")
	mux := http.NewServeMux()
	handler.Register(mux)
	body := `{"requests":[{"custom_id":"first","params":{"model":"` + model.ID + `","max_tokens":16,"messages":[{"role":"user","content":"one"}]}},{"custom_id":"second","params":{"model":"` + model.ID + `","max_tokens":16,"messages":[{"role":"user","content":"two"}]}}]}`
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, body)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d: %s", created.Code, created.Body.String())
	}
	var batch map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	id, _ := batch["id"].(string)
	if !strings.HasPrefix(id, "msgbatch_") || batch["processing_status"] != "in_progress" || batch["results_url"] != nil {
		t.Fatalf("created batch = %#v", batch)
	}
	for index := range 2 {
		job, ok := handler.claimMessageBatch(ctx)
		if !ok {
			t.Fatal("batch item was not claimable")
		}
		handler.runMessageBatch(ctx, job)
		if index == 0 {
			held := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+id, secret, "")
			var partial struct {
				Status string           `json:"processing_status"`
				Counts map[string]int64 `json:"request_counts"`
			}
			_ = json.Unmarshal(held.Body.Bytes(), &partial)
			if partial.Status != "in_progress" || partial.Counts["processing"] != 2 || partial.Counts["succeeded"] != 0 {
				t.Fatalf("partial counts = %#v", partial)
			}
		}
	}
	if _, ok := handler.claimMessageBatch(ctx); ok {
		t.Fatal("unexpected extra batch item")
	}
	retrieved := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+id, secret, "")
	if retrieved.Code != http.StatusOK {
		t.Fatalf("retrieve %d: %s", retrieved.Code, retrieved.Body.String())
	}
	if err := json.Unmarshal(retrieved.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if batch["processing_status"] != "ended" || batch["results_url"] != "https://gateway.example/api/anthropic/v1/messages/batches/"+id+"/results" {
		t.Fatalf("retrieved batch = %#v", batch)
	}
	results := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+id+"/results", secret, "")
	if results.Code != http.StatusOK || results.Header().Get("Content-Type") != "application/x-jsonlines" || strings.HasSuffix(results.Body.String(), "\n") {
		t.Fatalf("results %d %q: %q", results.Code, results.Header().Get("Content-Type"), results.Body.String())
	}
	lines := strings.Split(results.Body.String(), "\n")
	if len(lines) != 2 {
		t.Fatalf("result lines = %d", len(lines))
	}
	for _, line := range lines {
		var item map[string]any
		if json.Unmarshal([]byte(line), &item) != nil {
			t.Fatalf("invalid JSONL: %q", line)
		}
		result := item["result"].(map[string]any)
		if result["type"] != "succeeded" {
			t.Fatalf("result = %#v", result)
		}
	}
	var requestCount, attemptCount int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE key_id=?`, key.ID).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE request_id IN (SELECT id FROM requests WHERE key_id=?)`, key.ID).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 || attemptCount != 2 || calls != 2 {
		t.Fatalf("requests=%d attempts=%d calls=%d", requestCount, attemptCount, calls)
	}
	deleted := performMessageBatchRequest(t, mux, http.MethodDelete, "/api/anthropic/v1/messages/batches/"+id, secret, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestMessageBatchValidationOwnershipAndCancellation(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, creatorSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Creator", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{"messages:batches"}, ModelPatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	_, noChatSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No chat", Scopes: []string{"messages:batches"}, ModelPatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	invalid := []string{
		`{"requests":[]}`,
		`{"requests":[{"custom_id":"a","params":{},"unexpected":true}]}`,
		`{"requests":[{"custom_id":"same","params":{"model":"m"}},{"custom_id":"same","params":{"model":"m"}}]}`,
		`{"requests":[{"custom_id":"a","params":null}]}`,
		`{"requests":[{"custom_id":"a","params":[]}]}`,
		`{"requests":[{"custom_id":"a","params":{"model":"m"}},{"custom_id":"b","params":{"model":"m"}},{"custom_id":"c","params":{"model":"m"}},{"custom_id":"d","params":{"model":"m"}},{"custom_id":"e","params":{"model":"m"}}]}`,
	}
	for _, body := range invalid {
		response := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", creatorSecret, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid create %d: %s", response.Code, response.Body.String())
		}
		assertAnthropicRequestIDNull(t, response.Body.Bytes())
	}
	denied := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", noChatSecret, `{"requests":[{"custom_id":"a","params":{"model":"m"}}]}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing chat scope = %d: %s", denied.Code, denied.Body.String())
	}
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", creatorSecret, `{"requests":[{"custom_id":"a","params":{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}}]}`)
	var batch map[string]any
	_ = json.Unmarshal(created.Body.Bytes(), &batch)
	id := batch["id"].(string)
	missing := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+id, otherSecret, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("other key retrieve = %d: %s", missing.Code, missing.Body.String())
	}
	assertAnthropicRequestIDNull(t, missing.Body.Bytes())
	canceled := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches/"+id+"/cancel", creatorSecret, `{}`)
	if canceled.Code != http.StatusOK {
		t.Fatalf("cancel %d: %s", canceled.Code, canceled.Body.String())
	}
	_ = json.Unmarshal(canceled.Body.Bytes(), &batch)
	if batch["processing_status"] != "ended" || batch["cancel_initiated_at"] == nil {
		t.Fatalf("canceled batch = %#v", batch)
	}
	results := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+id+"/results", creatorSecret, "")
	if !strings.Contains(results.Body.String(), `"type":"canceled"`) {
		t.Fatalf("canceled result = %s", results.Body.String())
	}
}

func TestMessageBatchDefersNestedValidationPerItem(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(response, `{"id":"msg_ok","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Mixed batch", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	valid := `{"model":"` + model.ID + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"missing","params":{"model":"`+model.ID+`"}},{"custom_id":"stream","params":{"model":"`+model.ID+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stream":true}},{"custom_id":"good","params":`+valid+`}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	var batch struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &batch)
	for range 3 {
		job, ok := handler.claimMessageBatch(ctx)
		if !ok {
			t.Fatal("item was not claimable")
		}
		handler.runMessageBatch(ctx, job)
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	result := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+batch.ID+"/results", secret, "")
	if result.Code != http.StatusOK || strings.Count(result.Body.String(), `"type":"invalid_request_error"`) != 2 || !strings.Contains(result.Body.String(), `"request_id":null`) || !strings.Contains(result.Body.String(), `"type":"succeeded"`) {
		t.Fatalf("results = %d: %s", result.Code, result.Body.String())
	}
}

func TestMessageBatchAlreadyExpiredBeforeDispatch(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(response, `{"id":"msg_unexpected","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Expired batch", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"one","params":{"model":"`+model.ID+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	job, ok := handler.claimMessageBatch(ctx)
	if !ok {
		t.Fatal("item was not claimable")
	}
	job.createdAt = time.Now().Add(-messageBatchProcessingLifetime - time.Second).UnixMilli()
	handler.runMessageBatch(ctx, job)
	var itemState, batchState string
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT i.state,b.processing_status FROM message_batch_items i JOIN message_batches b ON b.id=i.batch_id WHERE i.batch_id=?`, job.batchID).Scan(&itemState, &batchState); err != nil {
		t.Fatal(err)
	}
	var requests int64
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE key_id=?`, job.keyID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || requests != 0 || itemState != "expired" || batchState != "ended" {
		t.Fatalf("calls=%d requests=%d item=%q batch=%q", calls, requests, itemState, batchState)
	}
}

func TestMessageBatchBeforePaginationUsesNearestNewerRows(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Pagination", Scopes: []string{"messages:batches"}, ModelPatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, id := range []string{"msgbatch_a", "msgbatch_b", "msgbatch_c"} {
		if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,processing_status,cancel_requested,created_at,ended_at,expires_at) VALUES(?,?,?,'ended',0,?,?,?)`, id, owner.ID, key.ID, now, now, now+messageBatchResultsLifetime.Milliseconds()); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	assertPage := func(path, want string, more bool) {
		t.Helper()
		response := performMessageBatchRequest(t, mux, http.MethodGet, path, secret, "")
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool `json:"has_more"`
		}
		if json.Unmarshal(response.Body.Bytes(), &page) != nil || response.Code != http.StatusOK || len(page.Data) != 1 || page.Data[0].ID != want || page.HasMore != more {
			t.Fatalf("page %s = %d %#v: %s", path, response.Code, page, response.Body.String())
		}
	}
	assertPage("/api/anthropic/v1/messages/batches?limit=1", "msgbatch_c", true)
	assertPage("/api/anthropic/v1/messages/batches?limit=1&after_id=msgbatch_c", "msgbatch_b", true)
	assertPage("/api/anthropic/v1/messages/batches?limit=1&after_id=msgbatch_b", "msgbatch_a", false)
	assertPage("/api/anthropic/v1/messages/batches?limit=1&before_id=msgbatch_a", "msgbatch_b", true)
	assertPage("/api/anthropic/v1/messages/batches?limit=1&before_id=msgbatch_b", "msgbatch_c", false)
}

func assertAnthropicRequestIDNull(t *testing.T, body []byte) {
	t.Helper()
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		t.Fatalf("invalid Anthropic error: %s", body)
	}
	requestID, exists := value["request_id"]
	if !exists || requestID != nil {
		t.Fatalf("request_id = %#v (exists %v): %s", requestID, exists, body)
	}
}

func TestMessageBatchCardinalityIsAppliedToChildAdmission(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		io.WriteString(response, `{"type":"message"}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "batch_items", Algorithm: "ceiling", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batch ceiling", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	item := `{"model":"` + model.ID + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"a","params":`+item+`},{"custom_id":"b","params":`+item+`}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d: %s", created.Code, created.Body.String())
	}
	for range 2 {
		job, ok := handler.claimMessageBatch(ctx)
		if !ok {
			t.Fatal("item was not claimable")
		}
		handler.runMessageBatch(ctx, job)
	}
	if calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls)
	}
	var errored int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batch_items WHERE state='errored'`).Scan(&errored); err != nil || errored != 2 {
		t.Fatalf("errored=%d err=%v", errored, err)
	}
}

func TestMessageBatchCompletedProviderOutcomeWinsCancellation(t *testing.T) {
	var markCanceled func()
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		markCanceled()
		_, _ = io.WriteString(response, `{"id":"msg_ok","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Completed batch", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"one","params":{"model":"`+model.ID+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	markCanceled = func() {
		now := time.Now().UnixMilli()
		if _, updateErr := store.SystemDB().ExecContext(ctx, `UPDATE message_batches SET processing_status='canceling',cancel_requested=1,cancel_initiated_at=? WHERE processing_status='in_progress'`, now); updateErr != nil {
			t.Errorf("mark canceled: %v", updateErr)
		}
	}
	job, ok := handler.claimMessageBatch(ctx)
	if !ok {
		t.Fatal("item was not claimable")
	}
	handler.runMessageBatch(ctx, job)
	var itemState, requestState, attemptState string
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT i.state,r.state,a.state FROM message_batch_items i JOIN requests r ON r.id=i.request_id JOIN attempts a ON a.id=i.attempt_id WHERE i.batch_id=?`, job.batchID).Scan(&itemState, &requestState, &attemptState); err != nil {
		t.Fatal(err)
	}
	if itemState != "succeeded" || requestState != "succeeded" || attemptState != "succeeded" {
		t.Fatalf("states = item %q request %q attempt %q", itemState, requestState, attemptState)
	}
}

func TestMessageBatchCancellationAndDeadlineSettleAccounting(t *testing.T) {
	for _, test := range []struct {
		name, want string
		cancel     bool
	}{
		{name: "cancellation", want: "canceled", cancel: true},
		{name: "deadline", want: "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
			}))
			defer upstream.Close()
			defer close(release)
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
			key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Interrupted batch", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1}); err != nil {
				t.Fatal(err)
			}
			handler := New(store.SystemDB(), keyService, providerService, usageService)
			mux := http.NewServeMux()
			handler.Register(mux)
			created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"one","params":{"model":"`+model.ID+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}}]}`)
			var batch struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(created.Body.Bytes(), &batch)
			job, ok := handler.claimMessageBatch(ctx)
			if !ok {
				t.Fatal("item was not claimable")
			}
			if !test.cancel {
				// Leave enough time for routing and admission on a cold CI runner; the
				// blocked upstream still proves that the processing deadline settles it.
				job.createdAt = time.Now().Add(-messageBatchProcessingLifetime + 3*time.Second).UnixMilli()
			}
			done := make(chan struct{})
			go func() { defer close(done); handler.runMessageBatch(ctx, job) }()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("upstream was not called")
			}
			if test.cancel {
				response := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches/"+batch.ID+"/cancel", secret, `{}`)
				if response.Code != http.StatusOK {
					t.Fatalf("cancel = %d: %s", response.Code, response.Body.String())
				}
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not finish")
			}
			var itemState, requestState, attemptState string
			if err = store.SystemDB().QueryRowContext(ctx, `SELECT i.state,r.state,a.state FROM message_batch_items i JOIN requests r ON r.id=i.request_id JOIN attempts a ON a.id=i.attempt_id WHERE i.batch_id=?`, batch.ID).Scan(&itemState, &requestState, &attemptState); err != nil {
				t.Fatal(err)
			}
			var leases int
			if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE request_id=(SELECT request_id FROM message_batch_items WHERE batch_id=?)`, batch.ID).Scan(&leases); err != nil {
				t.Fatal(err)
			}
			if itemState != test.want || requestState != "interrupted_unknown" || attemptState != "interrupted_unknown" || leases != 0 {
				t.Fatalf("states = item %q request %q attempt %q leases %d", itemState, requestState, attemptState, leases)
			}
		})
	}
}

func TestMessageBatchBoundsWrappedProviderError(t *testing.T) {
	largeError := `{"type":"error","error":{"type":"api_error","message":"` + strings.Repeat("x", maxInferenceBody-100) + `"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, largeError)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Large error", Scopes: []string{"messages:batches", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"one","params":{"model":"`+model.ID+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}}]}`)
	var batch struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &batch)
	job, ok := handler.claimMessageBatch(ctx)
	if !ok {
		t.Fatal("item was not claimable")
	}
	handler.runMessageBatch(ctx, job)
	var state string
	var size int
	var resultJSON []byte
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state,length(result_json),result_json FROM message_batch_items WHERE batch_id=?`, batch.ID).Scan(&state, &size, &resultJSON); err != nil {
		t.Fatal(err)
	}
	var line struct {
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resultJSON, &line)
	retrieved := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+batch.ID, secret, "")
	var value struct {
		Counts map[string]int64 `json:"request_counts"`
	}
	_ = json.Unmarshal(retrieved.Body.Bytes(), &value)
	if state != "errored" || line.Result.Type != "errored" || size > maxInferenceBody || value.Counts["errored"] != 1 || value.Counts["succeeded"] != 0 {
		t.Fatalf("state=%q result type=%q bytes=%d counts=%v", state, line.Result.Type, size, value.Counts)
	}
}

func performMessageBatchRequest(t *testing.T, handler http.Handler, method, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
