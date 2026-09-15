package gateway

import (
	"context"
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

func TestBackgroundConversationPublicationExcludesAgedRequestRepair(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation repair", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Aged","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &queuedResponse)
	if queued.Code != http.StatusOK || queuedResponse.ID == "" {
		t.Fatalf("queued=%d %s", queued.Code, queued.Body.String())
	}
	if _, err = store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_aged_background_completion BEFORE UPDATE OF state ON stored_responses WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	var requestID, attemptState string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = store.SystemDB().QueryRowContext(ctx, `SELECT stored_responses.request_id,attempts.state FROM stored_responses JOIN attempts ON attempts.request_id=stored_responses.request_id WHERE stored_responses.id=?`, queuedResponse.ID).Scan(&requestID, &attemptState)
		if err == nil && requestID != "" && attemptState == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requestID == "" || attemptState != "succeeded" {
		t.Fatalf("background request association=%q attempt=%q err=%v", requestID, attemptState, err)
	}
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE requests SET started_at=? WHERE id=?; UPDATE attempts SET finished_at=? WHERE request_id=?`, old, requestID, old, requestID); err != nil {
		t.Fatal(err)
	}
	repaired, err := usageService.RepairStaleRequests(ctx)
	if err != nil || repaired != 0 {
		t.Fatalf("active publication repaired=%d err=%v", repaired, err)
	}
	var requestState, responseState string
	var leases int
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, requestID).Scan(&requestState); err != nil || requestState != "in_progress" {
		t.Fatalf("request state=%s err=%v", requestState, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM stored_responses WHERE id=?`, queuedResponse.ID).Scan(&responseState); err != nil || responseState != "running" {
		t.Fatalf("response state=%s err=%v", responseState, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID).Scan(&leases); err != nil || leases != 1 {
		t.Fatalf("request leases=%d err=%v", leases, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DROP TRIGGER block_aged_background_completion`); err != nil {
		t.Fatal(err)
	}
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "completed")
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, requestID).Scan(&requestState); err != nil || requestState != "succeeded" {
		t.Fatalf("completed request state=%s err=%v", requestState, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("completed request leases=%d err=%v", leases, err)
	}
}

func TestBackgroundConversationRepairWinFailsJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation repair winner", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Repair winner","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &queuedResponse)
	if queued.Code != http.StatusOK || queuedResponse.ID == "" {
		t.Fatalf("queued=%d %s", queued.Code, queued.Body.String())
	}
	if _, err = store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_background_request_link BEFORE UPDATE OF request_id ON stored_responses WHEN NEW.request_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	var requestID, attemptState string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = store.SystemDB().QueryRowContext(ctx, `SELECT requests.id,attempts.state FROM requests JOIN attempts ON attempts.request_id=requests.id ORDER BY requests.started_at DESC LIMIT 1`).Scan(&requestID, &attemptState)
		if err == nil && requestID != "" && attemptState == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requestID == "" || attemptState != "succeeded" {
		t.Fatalf("background request=%q attempt=%q err=%v", requestID, attemptState, err)
	}
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE requests SET started_at=? WHERE id=?; UPDATE attempts SET finished_at=? WHERE request_id=?`, old, requestID, old, requestID); err != nil {
		t.Fatal(err)
	}
	repaired, err := usageService.RepairStaleRequests(ctx)
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DROP TRIGGER block_background_request_link`); err != nil {
		t.Fatal(err)
	}
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "failed")
	var requestState, body string
	var leases, items int
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT state FROM requests WHERE id=?`, requestID).Scan(&requestState); err != nil || requestState != "failed" {
		t.Fatalf("request state=%s err=%v", requestState, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT body_json FROM stored_responses WHERE id=?`, queuedResponse.ID).Scan(&body); err != nil || !strings.Contains(body, `"code":"accounting_failed"`) {
		t.Fatalf("response body=%s err=%v", body, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("request leases=%d err=%v", leases, err)
	}
	if err = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?`, conversation.ID).Scan(&items); err != nil || items != 0 {
		t.Fatalf("conversation items=%d err=%v", items, err)
	}
}
