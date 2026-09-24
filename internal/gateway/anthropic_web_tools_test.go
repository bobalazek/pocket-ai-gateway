package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestAnthropicCombinedWebToolsValidation(t *testing.T) {
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"tools":[{"type":"web_search_20260318","name":"web_search","max_uses":2},{"type":"web_fetch_20260318","name":"web_fetch","max_uses":3,"max_content_tokens":1024}]}`), &envelope)
	search, err := validateAnthropicWebSearch(envelope)
	if err != nil || !search.enabled || search.maxUses != 2 || !search.dynamic || !search.responseInclusion {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	fetch, err := validateAnthropicWebFetch(envelope)
	if err != nil || !fetch.enabled || fetch.maxUses != 3 || fetch.maxContentTokens != 1024 || !fetch.dynamic || !fetch.cacheBypass || !fetch.responseInclusion {
		t.Fatalf("fetch=%#v err=%v", fetch, err)
	}
}

func TestAnthropicCombinedWebToolsNativeAccounting(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	knownBody := `{"id":"msg_both","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"server_tool_use","id":"srvtoolu_s","name":"web_search","input":{"query":"news"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_s","content":[]},{"type":"server_tool_use","id":"srvtoolu_f","name":"web_fetch","input":{"url":"https://example.com"}},{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_f","content":{"type":"web_fetch_result","url":"https://example.com","content":{"type":"document","source":{"type":"text","media_type":"text/plain","data":"hi"}}}}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3,"server_tool_use":{"web_search_requests":1,"web_fetch_requests":2}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, knownBody)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-both")
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic both", Scopes: []string{"chat:generate", "messages:web_search", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	fee := "0.01"
	if _, err = usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: model.ID, InputUSDPerMillion: "1", OutputUSDPerMillion: "1", WebSearchUSDPerCall: &fee, Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-both","max_tokens":64,"messages":[{"role":"user","content":"Find and read https://example.com"}],"tools":[{"type":"web_search_20260318","name":"web_search","max_uses":2},{"type":"web_fetch_20260318","name":"web_fetch","max_uses":3,"max_content_tokens":1024}]}`
	response := performAnthropicRequest(t, mux, secret, body)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"model":"claude-both"`) || strings.Contains(response.Body.String(), `"model":"claude-upstream"`) || !strings.Contains(response.Body.String(), `"type":"web_search_tool_result"`) || !strings.Contains(response.Body.String(), `"type":"web_fetch_tool_result"`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	for _, want := range []string{`"model":"claude-upstream"`, `"type":"web_search_20260318"`, `"type":"web_fetch_20260318"`} {
		if !strings.Contains(string(forwarded), want) {
			t.Fatalf("forwarded body=%s", forwarded)
		}
	}
	var state, usageStatus, toolStatus string
	var input, output, maximum, searchCalls, responseTools, estimatedInput int64
	var fetchPresent bool
	var cost sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_max_calls,web_search_call_count,web_fetch_present,response_tool_call_count,tool_call_status,as_recorded_cost_nanos,estimated_tokens FROM attempts`).Scan(&state, &usageStatus, &input, &output, &maximum, &searchCalls, &fetchPresent, &responseTools, &toolStatus, &cost, &estimatedInput); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || input != 9 || output != 3 || maximum != 2 || searchCalls != 1 || !fetchPresent || responseTools != 3 || toolStatus != "completed" || cost.Valid || estimatedInput < 2*webSearchInputPerCall+3*1024 {
		t.Fatalf("accounting=%s/%s input=%d output=%d max=%d search=%d tools=%d/%s cost=%v estimate=%d", state, usageStatus, input, output, maximum, searchCalls, responseTools, toolStatus, cost, estimatedInput)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["chat:generate","messages:web_search"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	denied := performAnthropicRequest(t, mux, secret, body)
	if denied.Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("scope status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["chat:generate","messages:web_search","messages:web_fetch"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"}); err != nil {
		t.Fatal(err)
	}
	spendDenied := performAnthropicRequest(t, mux, secret, body)
	if spendDenied.Code != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("combined spend status=%d calls=%d body=%s", spendDenied.Code, calls.Load(), spendDenied.Body.String())
	}
}

func TestAnthropicCombinedWebToolsStreamAccounting(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-upstream\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_s\",\"name\":\"web_search\",\"input\":{\"query\":\"news\"}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_f\",\"name\":\"web_fetch\",\"input\":{\"url\":\"https://example.com\"}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":7,\"output_tokens\":2,\"server_tool_use\":{\"web_search_requests\":1,\"web_fetch_requests\":2}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	ctx, database, handler, secret, calls := anthropicCombinedWebToolsStreamFixture(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, stream)
	})
	result := performAnthropicRequest(t, handler, secret, `{"model":"claude-both","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Find and read https://example.com"}],"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":2},{"type":"web_fetch_20260209","name":"web_fetch","max_uses":3,"max_content_tokens":1024}]}`)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"model":"claude-both"`) || strings.Contains(result.Body.String(), `"model":"claude-upstream"`) || calls.Load() != 1 {
		t.Fatalf("response=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	var state, usageStatus, toolStatus string
	var input, output, searchCalls, tools int64
	if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count,response_tool_call_count,tool_call_status FROM attempts`).Scan(&state, &usageStatus, &input, &output, &searchCalls, &tools, &toolStatus); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || input != 7 || output != 2 || searchCalls != 1 || tools != 3 || toolStatus != "completed" {
		t.Fatalf("accounting=%s/%s input=%d output=%d search=%d tools=%d/%s", state, usageStatus, input, output, searchCalls, tools, toolStatus)
	}
}

func TestAnthropicCombinedWebToolsMixedCallers(t *testing.T) {
	var calls atomic.Int64
	knownBody := `{"id":"msg_mixed","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"server_tool_use","id":"srvtoolu_f","name":"web_fetch","input":{"url":"https://example.com"}}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"server_tool_use":{"web_search_requests":1,"web_fetch_requests":1,"code_execution_requests":1}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, knownBody)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-both")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic both", Scopes: []string{"chat:generate", "messages:web_search", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-both","max_tokens":64,"messages":[{"role":"user","content":"Find and read https://example.com"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"allowed_callers":["direct"]},{"type":"web_fetch_20260209","name":"web_fetch","max_uses":3,"max_content_tokens":1024}]}`
	response := performAnthropicRequest(t, mux, secret, body)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"code_execution_requests":1`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	var state, usageStatus string
	var searchCalls, tools int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,web_search_call_count,response_tool_call_count FROM attempts`).Scan(&state, &usageStatus, &searchCalls, &tools); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || searchCalls != 1 || tools != 2 {
		t.Fatalf("accounting=%s/%s search=%d tools=%d", state, usageStatus, searchCalls, tools)
	}
}

func TestAnthropicHostedWebToolsWithPromptCache(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	knownBody := `{"id":"msg_cached","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"server_tool_use","id":"srvtoolu_s","name":"web_search","input":{"query":"news"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_s","content":[]}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2,"cache_creation_input_tokens":10,"cache_read_input_tokens":6,"server_tool_use":{"web_search_requests":1}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, knownBody)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-both")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic cached", Scopes: []string{"chat:generate", "messages:web_search", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-both","max_tokens":64,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":2}]}`
	response := performAnthropicRequest(t, mux, secret, body)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"model":"claude-both"`) || !strings.Contains(response.Body.String(), `"type":"web_search_tool_result"`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	for _, want := range []string{`"model":"claude-upstream"`, `"type":"web_search_20260209"`, `"cache_control":{"type":"ephemeral"}`} {
		if !strings.Contains(string(forwarded), want) {
			t.Fatalf("forwarded body=%s", forwarded)
		}
	}
	var state, usageStatus, toolStatus string
	var input, output, searchCalls, tools int64
	var cacheCreation, cacheRead sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,cache_creation_input_tokens,cache_read_input_tokens,web_search_call_count,response_tool_call_count,tool_call_status FROM attempts`).Scan(&state, &usageStatus, &input, &output, &cacheCreation, &cacheRead, &searchCalls, &tools, &toolStatus); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || input != 20 || output != 2 || !cacheCreation.Valid || cacheCreation.Int64 != 10 || !cacheRead.Valid || cacheRead.Int64 != 6 || searchCalls != 1 || tools != 1 || toolStatus != "completed" {
		t.Fatalf("accounting=%s/%s input=%d output=%d cache=%v/%v search=%d tools=%d/%s", state, usageStatus, input, output, cacheCreation, cacheRead, searchCalls, tools, toolStatus)
	}
}

func anthropicCombinedWebToolsStreamFixture(t *testing.T, serve http.HandlerFunc) (context.Context, *sql.DB, http.Handler, string, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		serve(response, request)
	}))
	t.Cleanup(upstream.Close)
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	t.Cleanup(func() { _ = store.Close() })
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-both")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic both stream", Scopes: []string{"chat:generate", "messages:web_search", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	return ctx, store.SystemDB(), mux, secret, calls
}
