package gateway

import (
	"bytes"
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

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestAnthropicWebFetchValidation(t *testing.T) {
	valid := []string{
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1}]}`,
		`{"stream":true,"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":4,"max_content_tokens":16384,"allowed_domains":["example.com","docs.example.com"],"citations":{"enabled":true}}]}`,
		`{"stream":false,"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":2,"max_content_tokens":1024,"blocked_domains":[],"citations":{"enabled":false}}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"cache_control":{"type":"ephemeral"}}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["code_execution_20250825"],"strict":false}]}`,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"]}]}`,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":false}]}`,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":true}]}`,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":true,"response_inclusion":"excluded"}]}`,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"],"use_cache":false,"response_inclusion":"full"}]}`,
	}
	for _, raw := range valid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		request, err := validateAnthropicWebFetch(envelope)
		if err != nil || !request.enabled || request.maxUses < 1 || request.maxContentTokens < 1 {
			t.Fatalf("valid request rejected: %s: %#v, %v", raw, request, err)
		}
	}
	for raw, wantDynamic := range map[string]bool{
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`:                              true,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"]}]}`: false,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"]}]}`: false,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`:                              true,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["direct"]}]}`: false,
	} {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		request, err := validateAnthropicWebFetch(envelope)
		if err != nil || request.dynamic != wantDynamic {
			t.Fatalf("dynamic=%t, want %t: %v", request.dynamic, wantDynamic, err)
		}
	}
	for raw, want := range map[string]struct{ cacheBypass, responseInclusion bool }{
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`:                                 {false, false},
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`:                                 {true, false},
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`:                                 {true, true},
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"response_inclusion":"excluded"}]}`: {true, true},
	} {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		request, err := validateAnthropicWebFetch(envelope)
		if err != nil || request.cacheBypass != want.cacheBypass || request.responseInclusion != want.responseInclusion {
			t.Fatalf("version flags = %#v, want %#v: %v", request, want, err)
		}
	}

	invalid := []string{
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":0,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":5,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":0}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":16385}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"fetch","max_uses":1,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024},{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["example.com"],"blocked_domains":["blocked.example"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["https://example.com"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["example.com/path"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["*.example.com"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["a.example","b.example","c.example","d.example","e.example","f.example","g.example","h.example","i.example","j.example","k.example"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"citations":{}}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"citations":{"enabled":"true"}}]}`,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":null}]}`,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_callers":["other"]}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"defer_loading":false}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"strict":"true"}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"strict":null}]}`,
		`{"tools":[{"type":"web_fetch_20260209","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":false}]}`,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":null}]}`,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"use_cache":"false"}]}`,
		`{"tools":[{"type":"web_fetch_20260309","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"response_inclusion":"excluded"}]}`,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"response_inclusion":"other"}]}`,
		`{"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"response_inclusion":null}]}`,
	}
	for _, raw := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		if _, err := validateAnthropicWebFetch(envelope); err == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
}

func TestAnthropicWebFetchUsageAndStreamParsing(t *testing.T) {
	for _, test := range []struct {
		raw      string
		max      int64
		want     *int64
		exceeded bool
	}{
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_fetch_requests":0,"web_search_requests":0}}}`, 1, int64Pointer(0), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_fetch_requests":4,"web_search_requests":0}}}`, 4, int64Pointer(4), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":null}}`, 4, int64Pointer(0), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2}}`, 4, nil, false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_fetch_requests":"1"}}}`, 4, nil, false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_fetch_requests":1,"web_search_requests":1}}}`, 4, nil, false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_fetch_requests":2}}}`, 1, nil, true},
	} {
		got, known, exceeded := parseAnthropicWebFetchUsage([]byte(test.raw), test.max, false, "")
		if (test.want != nil) != known || test.want != nil && (got == nil || *got != *test.want) || exceeded != test.exceeded {
			t.Fatalf("parse %s = %v/%t/%t, want %v exceeded=%t", test.raw, got, known, exceeded, test.want, test.exceeded)
		}
	}
	got, known, exceeded := parseAnthropicWebFetchUsage([]byte(`{"usage":{"server_tool_use":{"web_fetch_requests":1,"code_execution_requests":1}}}`), 1, true, "")
	if !known || exceeded || got == nil || *got != 1 {
		t.Fatalf("dynamic usage=%v/%t/%t", got, known, exceeded)
	}
	if got, known, exceeded := parseAnthropicWebFetchUsage([]byte(`{"usage":{"server_tool_use":{"web_fetch_requests":1,"web_search_requests":1}}}`), 2, false, "web_search_requests"); !known || exceeded || got == nil || *got != 1 {
		t.Fatalf("companion search usage=%v/%t/%t", got, known, exceeded)
	}

	valid := anthropicWebFetchSSE(`{"web_fetch_requests":1,"web_search_requests":0}`, "")
	count, err := parseAnthropicWebFetchStream([]byte(valid), 1, false, "")
	if err != nil || count == nil || *count != 1 {
		t.Fatalf("valid stream count=%v err=%v", count, err)
	}
	nullableTerminalInput := strings.Replace(valid, `"input_tokens":7,"output_tokens":2`, `"input_tokens":null,"output_tokens":2`, 1)
	count, err = parseAnthropicWebFetchStream([]byte(nullableTerminalInput), 1, false, "")
	if err != nil || count == nil || *count != 1 {
		t.Fatalf("nullable terminal input count=%v err=%v", count, err)
	}
	for name, raw := range map[string]string{
		"missing start":    strings.SplitN(valid, "\n\n", 2)[1],
		"missing stop":     strings.TrimSuffix(valid, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		"missing usage":    anthropicWebFetchSSE("", ""),
		"malformed":        anthropicWebFetchSSE(`{"web_fetch_requests":"one"}`, ""),
		"overrun":          anthropicWebFetchSSE(`{"web_fetch_requests":2}`, ""),
		"missing output":   strings.Replace(valid, `,"output_tokens":2`, "", 1),
		"null output":      strings.Replace(valid, `"output_tokens":2`, `"output_tokens":null`, 1),
		"other tool usage": strings.Replace(valid, `"web_search_requests":0`, `"web_search_requests":1`, 1),
		"error":            "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAnthropicWebFetchStream([]byte(raw), 1, false, ""); err == nil {
				t.Fatalf("invalid stream accepted: %s", raw)
			}
		})
	}
	combined := anthropicWebFetchSSE(`{"web_fetch_requests":1,"web_search_requests":2}`, "")
	if count, err := parseAnthropicWebFetchStream([]byte(combined), 2, false, "web_search_requests"); err != nil || count == nil || *count != 1 {
		t.Fatalf("combined stream count=%v err=%v", count, err)
	}
}

func TestAnthropicWebFetchTargetEligibility(t *testing.T) {
	valid := providers.Target{PublicModel: providers.PublicModel{Adapter: "anthropic", Capabilities: []string{"chat", "web_fetch"}, RoutingStrategy: "fixed"}, UpstreamCapabilities: []string{"chat", "web_fetch"}, Preset: "anthropic"}
	if eligible, reason := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{}); !eligible {
		t.Fatalf("valid target rejected: %s", reason)
	}
	if eligible, _ := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{dynamic: true}); eligible {
		t.Fatal("dynamic fetch accepted without its model capability")
	}
	valid.Capabilities = append(valid.Capabilities, "web_fetch_dynamic")
	valid.UpstreamCapabilities = append(valid.UpstreamCapabilities, "web_fetch_dynamic")
	if eligible, reason := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{dynamic: true}); !eligible {
		t.Fatalf("dynamic target rejected: %s", reason)
	}
	if eligible, _ := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{dynamic: true, cacheBypass: true}); eligible {
		t.Fatal("cache bypass accepted without its model capability")
	}
	valid.Capabilities = append(valid.Capabilities, "web_fetch_cache_bypass")
	valid.UpstreamCapabilities = append(valid.UpstreamCapabilities, "web_fetch_cache_bypass")
	if eligible, _ := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{dynamic: true, cacheBypass: true, responseInclusion: true}); eligible {
		t.Fatal("response inclusion accepted without its model capability")
	}
	valid.Capabilities = append(valid.Capabilities, "web_fetch_response_inclusion")
	valid.UpstreamCapabilities = append(valid.UpstreamCapabilities, "web_fetch_response_inclusion")
	if eligible, reason := anthropicWebFetchTargetEligibility(valid, anthropicWebFetchRequest{dynamic: true, cacheBypass: true, responseInclusion: true}); !eligible {
		t.Fatalf("response-inclusion target rejected: %s", reason)
	}
	for name, mutate := range map[string]func(*providers.Target){
		"custom preset":               func(target *providers.Target) { target.Preset = "custom" },
		"translated adapter":          func(target *providers.Target) { target.Adapter = "openai" },
		"missing public capability":   func(target *providers.Target) { target.Capabilities = []string{"chat"} },
		"missing upstream capability": func(target *providers.Target) { target.UpstreamCapabilities = []string{"chat"} },
		"free only":                   func(target *providers.Target) { target.FreeOnly = true },
		"lowest cost":                 func(target *providers.Target) { target.RoutingStrategy = "lowest_cost" },
	} {
		t.Run(name, func(t *testing.T) {
			target := valid
			mutate(&target)
			if eligible, _ := anthropicWebFetchTargetEligibility(target, anthropicWebFetchRequest{}); eligible {
				t.Fatalf("ineligible target accepted: %#v", target)
			}
		})
	}
}

func TestAnthropicWebFetchNativeAccountingAndBoundaries(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	var malformed, overLimit atomic.Bool
	knownBody := `{"id":"msg_fetch","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"server_tool_use","id":"srvtoolu_code","name":"code_execution","input":{"code":"await web_fetch(...)"}},{"type":"server_tool_use","id":"srvtoolu_1","name":"web_fetch","input":{"url":"https://example.com"},"caller":{"type":"code_execution_20260120","tool_id":"srvtoolu_code"}},{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_1","caller":{"type":"code_execution_20260120","tool_id":"srvtoolu_code"},"content":{"type":"web_fetch_result","url":"https://example.com","retrieved_at":"2026-09-16T08:00:00Z","content":{"type":"document","source":{"type":"text","media_type":"text/plain","data":"page"},"title":"Example","citations":{"enabled":true}}}},{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_code","content":{"type":"code_execution_result","stdout":"","stderr":"","return_code":0,"content":[]}}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3,"server_tool_use":{"web_fetch_requests":1,"web_search_requests":0,"code_execution_requests":1}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		switch {
		case overLimit.Load():
			_, _ = io.WriteString(response, strings.Replace(knownBody, `"web_fetch_requests":1`, `"web_fetch_requests":2`, 1))
		case malformed.Load():
			_, _ = io.WriteString(response, strings.Replace(knownBody, `"web_fetch_requests":1`, `"web_fetch_requests":"one"`, 1))
		default:
			_, _ = io.WriteString(response, knownBody)
		}
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-fetch")
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic fetch", Scopes: []string{"chat:generate", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: model.ID, InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-fetch","max_tokens":64,"messages":[{"role":"user","content":"Fetch https://example.com"}],"tools":[{"type":"web_fetch_20260318","name":"web_fetch","max_uses":1,"max_content_tokens":1024,"allowed_domains":["example.com"],"citations":{"enabled":true},"use_cache":false,"response_inclusion":"excluded"}]}`
	response := performAnthropicRequest(t, mux, secret, body)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"model":"claude-fetch"`) || strings.Contains(response.Body.String(), `"model":"claude-upstream"`) || !strings.Contains(response.Body.String(), `"type":"web_fetch_tool_result"`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if !bytes.Contains(forwarded, []byte(`"model":"claude-upstream"`)) || !bytes.Contains(forwarded, []byte(`"type":"web_fetch_20260318"`)) || !bytes.Contains(forwarded, []byte(`"use_cache":false`)) || !bytes.Contains(forwarded, []byte(`"response_inclusion":"excluded"`)) {
		t.Fatalf("forwarded body=%s", forwarded)
	}
	var state, usageStatus, toolStatus string
	var input, output, requestTools, responseTools, estimated int64
	var searchMax, searchCalls, cost sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,request_tool_count,response_tool_call_count,tool_call_status,web_search_max_calls,web_search_call_count,as_recorded_cost_nanos,estimated_tokens FROM attempts`).Scan(&state, &usageStatus, &input, &output, &requestTools, &responseTools, &toolStatus, &searchMax, &searchCalls, &cost, &estimated); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || input != 9 || output != 3 || requestTools != 1 || responseTools != 2 || toolStatus != "completed" || searchMax.Valid || searchCalls.Valid || !cost.Valid || cost.Int64 != 12_000 || estimated < 1024 {
		t.Fatalf("accounting=%s/%s input=%d output=%d tools=%d/%d/%s search=%v/%v cost=%v estimate=%d", state, usageStatus, input, output, requestTools, responseTools, toolStatus, searchMax, searchCalls, cost, estimated)
	}

	malformed.Store(true)
	unknown := performAnthropicRequest(t, mux, secret, body)
	if unknown.Code != http.StatusOK {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	var unknownState, unknownStatus string
	var unknownInput, unknownOutput sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens FROM attempts ORDER BY rowid DESC LIMIT 1`).Scan(&unknownState, &unknownStatus, &unknownInput, &unknownOutput); err != nil {
		t.Fatal(err)
	}
	if unknownState != "succeeded" || unknownStatus != "unknown" || unknownInput.Valid || unknownOutput.Valid {
		t.Fatalf("unknown accounting=%s/%s input=%v output=%v", unknownState, unknownStatus, unknownInput, unknownOutput)
	}

	malformed.Store(false)
	overLimit.Store(true)
	over := performAnthropicRequest(t, mux, secret, body)
	if over.Code != http.StatusBadGateway || strings.Contains(over.Body.String(), "msg_fetch") {
		t.Fatalf("over-limit status=%d body=%s", over.Code, over.Body.String())
	}

	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["chat:generate"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	denied := performAnthropicRequest(t, mux, secret, body)
	if denied.Code != http.StatusForbidden || calls.Load() != 3 {
		t.Fatalf("scope status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
}

func TestAnthropicWebFetchIsRejectedInMessageBatches(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-fetch")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Fetch batch", Scopes: []string{"messages:batches", "chat:generate", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, `{"requests":[{"custom_id":"fetch","params":{"model":"claude-fetch","max_tokens":8,"messages":[{"role":"user","content":"Fetch"}],"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create=%d body=%s", created.Code, created.Body.String())
	}
	job, ok := handler.claimMessageBatch(ctx)
	if !ok {
		t.Fatal("batch item was not claimable")
	}
	handler.runMessageBatch(ctx, job)
	results := performMessageBatchRequest(t, mux, http.MethodGet, "/api/anthropic/v1/messages/batches/"+job.batchID+"/results", secret, "")
	if results.Code != http.StatusOK || !strings.Contains(results.Body.String(), `"type":"errored"`) || !strings.Contains(results.Body.String(), "web fetch is not supported") || calls.Load() != 0 {
		t.Fatalf("results=%d calls=%d body=%s", results.Code, calls.Load(), results.Body.String())
	}
}

func TestAnthropicWebFetchDoesNotFallbackAfterDispatch(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(response, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = io.WriteString(response, `{"id":"unexpected"}`)
	}))
	defer second.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, firstUpstream, model := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1", "first", "claude-fetch")
	secondConnection, secondUpstream, _ := publishAnthropicWebFetchModel(t, ctx, store.SystemDB(), providerService, owner, second.URL+"/v1", "second", "")
	model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No fallback", Scopes: []string{"chat:generate", "messages:web_fetch"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	response := performAnthropicRequest(t, mux, secret, `{"model":"claude-fetch","max_tokens":8,"messages":[{"role":"user","content":"Fetch"}],"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":1,"max_content_tokens":1024}]}`)
	if response.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("status=%d first=%d second=%d body=%s", response.Code, firstCalls.Load(), secondCalls.Load(), response.Body.String())
	}
}

func anthropicWebFetchSSE(serverUsage, content string) string {
	usage := `{"input_tokens":7,"output_tokens":2}`
	if serverUsage != "" {
		usage = `{"input_tokens":7,"output_tokens":2,"server_tool_use":` + serverUsage + `}`
	}
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-upstream\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" + content + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":" + usage + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func publishAnthropicWebFetchModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID, publicID string) (providers.Connection, providers.UpstreamModel, providers.PublicModel) {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: upstreamID, Preset: "anthropic", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", baseURL, connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"chat", "prompt_cache", "web_search", "web_search_dynamic", "web_search_response_inclusion", "web_fetch", "web_fetch_dynamic", "web_fetch_cache_bypass", "web_fetch_response_inclusion"})
	if err != nil {
		t.Fatal(err)
	}
	var model providers.PublicModel
	if publicID != "" {
		model, err = service.CreatePublicModel(ctx, owner, publicID, "Anthropic web fetch", "", upstream.ID, upstream.Capabilities)
		if err != nil {
			t.Fatal(err)
		}
	}
	return connection, upstream, model
}
