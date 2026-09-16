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

func TestAnthropicWebSearchValidation(t *testing.T) {
	valid := []string{
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"stream":false,"tools":[{"name":"weather","description":"Local weather","input_schema":{"type":"object"}},{"type":"custom","name":"clock","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","max_uses":4,"allowed_domains":["example.com","docs.example.com/guides/*"],"user_location":{"type":"approximate","city":"Ljubljana","country":"SI","region":null,"timezone":"Europe/Ljubljana"},"allowed_callers":["direct"]}]}`,
		`{"stream":null,"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"blocked_domains":[]}]}`,
		`{"stream":true,"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_domains":null,"blocked_domains":null,"user_location":null}]}`,
	}
	for _, raw := range valid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		request, err := validateAnthropicWebSearch(envelope)
		if err != nil || !request.enabled || request.maxUses < 1 {
			t.Fatalf("valid request rejected: %s: %#v, %v", raw, request, err)
		}
	}
	invalid := []string{
		`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":0}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"search","max_uses":1}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1},{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":1}]}`,
		`{"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_domains":["example.com"],"blocked_domains":["ads.example"]}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_domains":["https://example.com"]}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_domains":["*.example.com"]}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"approximate"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"exact","city":"Ljubljana"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"approximate","country":"SVN"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"approximate","country":"ZZ"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"approximate","timezone":"Local"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"user_location":{"type":"approximate","timezone":"Europe/Not_A_Zone"}}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_callers":null}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_callers":[]}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1,"allowed_callers":["code_execution_20260120"]}]}`,
		`{"stream":"true","tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"stream":1,"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"stream":[],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
		`{"stream":{},"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
	}
	for _, raw := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		if _, err := validateAnthropicWebSearch(envelope); err == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
}

func TestAnthropicWebSearchUsageParsing(t *testing.T) {
	for _, test := range []struct {
		raw      string
		max      int64
		want     *int64
		exceeded bool
	}{
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":0}}}`, 1, int64Pointer(0), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":4}}}`, 4, int64Pointer(4), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":null}}`, 4, int64Pointer(0), false},
		{`{"usage":{"input_tokens":1,"output_tokens":2}}`, 4, nil, false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":"1"}}}`, 4, nil, false},
		{`{"usage":{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":2}}}`, 1, nil, true},
	} {
		got, known, exceeded := parseAnthropicWebSearchUsage([]byte(test.raw), test.max)
		if (test.want != nil) != known || test.want != nil && (got == nil || *got != *test.want) || exceeded != test.exceeded {
			t.Fatalf("parse %s = %v/%t/%t, want %v exceeded=%t", test.raw, got, known, exceeded, test.want, test.exceeded)
		}
	}
}

func TestAnthropicWebSearchStreamParsing(t *testing.T) {
	valid := anthropicWebSearchSSE("end_turn", `{"web_search_requests":2}`, "")
	decreasing := strings.Replace(anthropicWebSearchSSE("end_turn", `{"web_search_requests":1}`, ""), "event: message_delta", "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1,\"server_tool_use\":{\"web_search_requests\":2}}}\n\nevent: message_delta", 1)
	count, err := parseAnthropicWebSearchStream([]byte(valid), 2)
	if err != nil || count == nil || *count != 2 {
		t.Fatalf("valid stream count=%v err=%v", count, err)
	}
	count, err = parseAnthropicWebSearchStream([]byte(strings.ReplaceAll(valid, "\n", "\r\n")), 2)
	if err != nil || count == nil || *count != 2 {
		t.Fatalf("valid CRLF stream count=%v err=%v", count, err)
	}
	for name, raw := range map[string]string{
		"missing start":    strings.SplitN(valid, "\n\n", 2)[1],
		"missing stop":     strings.TrimSuffix(valid, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		"missing usage":    anthropicWebSearchSSE("end_turn", "", ""),
		"malformed":        anthropicWebSearchSSE("end_turn", `{"web_search_requests":"two"}`, ""),
		"overrun":          anthropicWebSearchSSE("end_turn", `{"web_search_requests":3}`, ""),
		"decreasing count": decreasing,
		"mismatched type":  strings.Replace(valid, "event: message_delta", "event: content_block_delta", 1),
		"error":            "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAnthropicWebSearchStream([]byte(raw), 2); err == nil {
				t.Fatalf("invalid stream accepted: %s", raw)
			}
		})
	}
	increasing := strings.Replace(anthropicWebSearchSSE("end_turn", `{"web_search_requests":2}`, ""), "event: message_delta", "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1,\"server_tool_use\":{\"web_search_requests\":1}}}\n\nevent: message_delta", 1)
	count, err = parseAnthropicWebSearchStream([]byte(increasing), 2)
	if err != nil || count == nil || *count != 2 {
		t.Fatalf("increasing cumulative count=%v err=%v", count, err)
	}
	newlineHeavy := append(bytes.Repeat([]byte{'\n'}, 1<<20), []byte("event: error\ndata: {}\n\n")...)
	if _, err := parseAnthropicWebSearchStream(newlineHeavy, 2); err == nil {
		t.Fatal("newline-heavy error stream accepted")
	}
}

func TestAnthropicWebSearchTargetEligibility(t *testing.T) {
	valid := providers.Target{PublicModel: providers.PublicModel{Adapter: "anthropic", Capabilities: []string{"chat", "web_search"}, RoutingStrategy: "fixed"}, UpstreamCapabilities: []string{"chat", "web_search"}, Preset: "anthropic"}
	if eligible, reason := anthropicWebSearchTargetEligibility(valid); !eligible {
		t.Fatalf("valid target rejected: %s", reason)
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
			if eligible, _ := anthropicWebSearchTargetEligibility(target); eligible {
				t.Fatalf("ineligible target accepted: %#v", target)
			}
		})
	}
}

func TestAnthropicWebSearchNativeAccountingAndUnknownUsage(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	var malformed atomic.Bool
	var overLimit atomic.Bool
	knownBody := `{"id":"msg_search","type":"message","role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"news"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},{"type":"tool_use","id":"toolu_1","name":"local","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":9,"output_tokens":3,"server_tool_use":{"web_search_requests":1}}}`
	unknownBody := `{"id":"msg_search_unknown","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3,"server_tool_use":{"web_search_requests":"one"}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		if overLimit.Load() {
			_, _ = io.WriteString(response, `{"id":"msg_search_excess","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3,"server_tool_use":{"web_search_requests":3}}}`)
			return
		}
		if malformed.Load() {
			_, _ = io.WriteString(response, unknownBody)
			return
		}
		_, _ = io.WriteString(response, knownBody)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-search")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Anthropic search", Scopes: []string{"chat:generate", "messages:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-search","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"allowed_domains":["example.com/news/*"],"allowed_callers":["direct"]},{"name":"local","input_schema":{"type":"object"}}]}`
	response := performAnthropicRequest(t, mux, secret, body)
	if response.Code != http.StatusOK || calls.Load() != 1 || response.Body.String() != knownBody {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	var forwardedEnvelope map[string]json.RawMessage
	if json.Unmarshal(forwarded, &forwardedEnvelope) != nil {
		t.Fatalf("forwarded body=%s", forwarded)
	}
	var upstreamModel string
	if json.Unmarshal(forwardedEnvelope["model"], &upstreamModel) != nil || upstreamModel != "claude-upstream" || !bytes.Contains(forwarded, []byte(`"encrypted_content"`)) && !bytes.Contains(forwarded, []byte(`"allowed_domains"`)) {
		t.Fatalf("forwarded body=%s", forwarded)
	}
	var state, usageStatus, toolStatus string
	var input, output, maximum, searchCalls, responseTools, estimatedInput int64
	var cost sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_max_calls,web_search_call_count,response_tool_call_count,tool_call_status,as_recorded_cost_nanos,estimated_tokens FROM attempts`).Scan(&state, &usageStatus, &input, &output, &maximum, &searchCalls, &responseTools, &toolStatus, &cost, &estimatedInput); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || input != 9 || output != 3 || maximum != 2 || searchCalls != 1 || responseTools != 2 || toolStatus != "completed" || cost.Valid || estimatedInput < 2*webSearchInputPerCall {
		t.Fatalf("accounting=%s/%s input=%d output=%d max=%d search=%d tools=%d/%s cost=%v estimate=%d", state, usageStatus, input, output, maximum, searchCalls, responseTools, toolStatus, cost, estimatedInput)
	}
	malformed.Store(true)
	unknown := performAnthropicRequest(t, mux, secret, body)
	if unknown.Code != http.StatusOK || unknown.Body.String() != unknownBody {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	var unknownState, unknownStatus string
	var unknownInput, unknownOutput, unknownCalls sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count FROM attempts ORDER BY rowid DESC LIMIT 1`).Scan(&unknownState, &unknownStatus, &unknownInput, &unknownOutput, &unknownCalls); err != nil {
		t.Fatal(err)
	}
	if unknownState != "succeeded" || unknownStatus != "unknown" || unknownInput.Valid || unknownOutput.Valid || unknownCalls.Valid {
		t.Fatalf("unknown accounting=%s/%s input=%v output=%v calls=%v", unknownState, unknownStatus, unknownInput, unknownOutput, unknownCalls)
	}
	malformed.Store(false)
	overLimit.Store(true)
	over := performAnthropicRequest(t, mux, secret, body)
	if over.Code != http.StatusBadGateway || strings.Contains(over.Body.String(), "msg_search_excess") {
		t.Fatalf("over-limit status=%d body=%s", over.Code, over.Body.String())
	}
	var overState, overStatus string
	var overCalls sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,web_search_call_count FROM attempts ORDER BY rowid DESC LIMIT 1`).Scan(&overState, &overStatus, &overCalls); err != nil {
		t.Fatal(err)
	}
	if overState != "failed" || overStatus != "unknown" || overCalls.Valid {
		t.Fatalf("over-limit accounting=%s/%s calls=%v", overState, overStatus, overCalls)
	}
}

func TestAnthropicWebSearchScopePromptCacheAndSpendPolicyPreventDispatch(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{"id":"msg","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1,"server_tool_use":{"web_search_requests":0}}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-upstream", "claude-search")
	key, unscoped, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No search", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"claude-search","max_tokens":8,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`
	denied := performAnthropicRequest(t, mux, unscoped, body)
	if denied.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("scope status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["chat:generate","messages:web_search"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	promptCache := performAnthropicRequest(t, mux, unscoped, `{"model":"claude-search","max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)
	if promptCache.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("cache status=%d calls=%d body=%s", promptCache.Code, calls.Load(), promptCache.Body.String())
	}
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "100"}); err != nil {
		t.Fatal(err)
	}
	spend := performAnthropicRequest(t, mux, unscoped, body)
	if spend.Code != http.StatusTooManyRequests || calls.Load() != 0 {
		t.Fatalf("spend status=%d calls=%d body=%s", spend.Code, calls.Load(), spend.Body.String())
	}
}

func TestAnthropicWebSearchDoesNotFallbackAfterDispatch(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	var embeddedError atomic.Bool
	embeddedBody := `{"id":"msg_search_error","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"News"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}}],"stop_reason":"pause_turn","usage":{"input_tokens":11,"output_tokens":2,"server_tool_use":null}}`
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		if embeddedError.Load() {
			_, _ = io.WriteString(response, embeddedBody)
			return
		}
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
	firstConnection, firstUpstream, model := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1", "first", "claude-search")
	secondConnection, secondUpstream, _ := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, second.URL+"/v1", "second", "")
	model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No fallback", Scopes: []string{"chat:generate", "messages:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	response := performAnthropicRequest(t, mux, secret, `{"model":"claude-search","max_tokens":8,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "Provider rejected the request") || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("status=%d first=%d second=%d body=%s", response.Code, firstCalls.Load(), secondCalls.Load(), response.Body.String())
	}
	var state, usageStatus string
	var input, output, calls sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count FROM attempts`).Scan(&state, &usageStatus, &input, &output, &calls); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || usageStatus != "unknown" || input.Valid || output.Valid || calls.Valid {
		t.Fatalf("failure accounting=%s/%s input=%v output=%v calls=%v", state, usageStatus, input, output, calls)
	}
	embeddedError.Store(true)
	embedded := performAnthropicRequest(t, mux, secret, `{"model":"claude-search","max_tokens":8,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)
	if embedded.Code != http.StatusOK || embedded.Body.String() != embeddedBody || firstCalls.Load() != 2 || secondCalls.Load() != 0 {
		t.Fatalf("embedded status=%d first=%d second=%d body=%s", embedded.Code, firstCalls.Load(), secondCalls.Load(), embedded.Body.String())
	}
	var embeddedState, embeddedUsageStatus, embeddedToolStatus string
	var embeddedInput, embeddedOutput, embeddedCalls int64
	var embeddedCost sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count,as_recorded_cost_nanos,tool_call_status FROM attempts ORDER BY rowid DESC LIMIT 1`).Scan(&embeddedState, &embeddedUsageStatus, &embeddedInput, &embeddedOutput, &embeddedCalls, &embeddedCost, &embeddedToolStatus); err != nil {
		t.Fatal(err)
	}
	if embeddedState != "succeeded" || embeddedUsageStatus != "provider_reported" || embeddedInput != 11 || embeddedOutput != 2 || embeddedCalls != 0 || embeddedCost.Valid || embeddedToolStatus != "none" {
		t.Fatalf("embedded accounting=%s/%s input=%d output=%d calls=%d cost=%v tool=%s", embeddedState, embeddedUsageStatus, embeddedInput, embeddedOutput, embeddedCalls, embeddedCost, embeddedToolStatus)
	}
}

func TestAnthropicWebSearchStreamingSuccess(t *testing.T) {
	for _, test := range []struct {
		name, stop, serverUsage, content string
		calls, tools                     int64
	}{
		{
			name:        "zero search embedded error",
			stop:        "pause_turn",
			serverUsage: "null",
			content:     "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srvtoolu_1\",\"caller\":{\"type\":\"direct\"},\"content\":{\"type\":\"web_search_tool_result_error\",\"error_code\":\"too_many_requests\"}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		},
		{name: "completed search", stop: "end_turn", serverUsage: `{"web_search_requests":1}`, content: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_1\",\"name\":\"web_search\",\"input\":{\"query\":\"News\"}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", calls: 1, tools: 1},
		{name: "search plus function tool", stop: "tool_use", serverUsage: `{"web_search_requests":1}`, content: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"local\",\"input\":{}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", calls: 1, tools: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := anthropicWebSearchSSE(test.stop, test.serverUsage, test.content)
			ctx, database, handler, secret, firstCalls, secondCalls := anthropicWebSearchStreamFixture(t, func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(response, stream)
			})
			result := performAnthropicRequest(t, handler, secret, `{"model":"claude-search","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}]}`)
			if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"model":"claude-search"`) || strings.Contains(result.Body.String(), `"model":"claude-upstream"`) || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
				t.Fatalf("response=%d first=%d second=%d body=%s", result.Code, firstCalls.Load(), secondCalls.Load(), result.Body.String())
			}
			var state, usageStatus, toolStatus string
			var input, output, searchCalls, tools int64
			var cost sql.NullInt64
			if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count,response_tool_call_count,tool_call_status,as_recorded_cost_nanos FROM attempts`).Scan(&state, &usageStatus, &input, &output, &searchCalls, &tools, &toolStatus, &cost); err != nil {
				t.Fatal(err)
			}
			if state != "succeeded" || usageStatus != "provider_reported" || input != 7 || output != 2 || searchCalls != test.calls || tools != test.tools || cost.Valid || toolStatus != map[bool]string{true: "completed", false: "none"}[test.tools > 0] {
				t.Fatalf("accounting=%s/%s input=%d output=%d search=%d tools=%d/%s cost=%v", state, usageStatus, input, output, searchCalls, tools, toolStatus, cost)
			}
		})
	}
}

func TestAnthropicWebSearchStreamingFailuresDoNotFallback(t *testing.T) {
	valid := anthropicWebSearchSSE("end_turn", `{"web_search_requests":1}`, "")
	for name, stream := range map[string]string{
		"overrun":         anthropicWebSearchSSE("end_turn", `{"web_search_requests":2}`, ""),
		"truncated":       strings.TrimSuffix(valid, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		"missing usage":   anthropicWebSearchSSE("end_turn", "", ""),
		"missing input":   strings.ReplaceAll(valid, `"input_tokens":7,`, ""),
		"missing output":  strings.Replace(valid, `"output_tokens":2`, `"output_tokens":null`, 1),
		"malformed usage": anthropicWebSearchSSE("end_turn", `{"web_search_requests":"one"}`, ""),
		"in-stream error": "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, database, handler, secret, firstCalls, secondCalls := anthropicWebSearchStreamFixture(t, func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(response, stream)
			})
			result := performAnthropicRequest(t, handler, secret, `{"model":"claude-search","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)
			if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"model":"claude-search"`) || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
				t.Fatalf("response=%d first=%d second=%d body=%s", result.Code, firstCalls.Load(), secondCalls.Load(), result.Body.String())
			}
			var state, usageStatus string
			var input, output, searchCalls sql.NullInt64
			if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count FROM attempts`).Scan(&state, &usageStatus, &input, &output, &searchCalls); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || usageStatus != "unknown" || input.Valid || output.Valid || searchCalls.Valid {
				t.Fatalf("accounting=%s/%s input=%v output=%v search=%v", state, usageStatus, input, output, searchCalls)
			}
		})
	}
}

func TestAnthropicWebSearchStreamingCancellationIsInterrupted(t *testing.T) {
	started := make(chan struct{}, 1)
	ctx, database, handler, secret, firstCalls, secondCalls := anthropicWebSearchStreamFixture(t, func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n")
		response.(http.Flusher).Flush()
		started <- struct{}{}
		<-request.Context().Done()
	})
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/anthropic/v1/messages", strings.NewReader(`{"model":"claude-search","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"News"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)).WithContext(requestContext)
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stream did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled gateway request did not stop")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	var state, usageStatus string
	var input, output, searchCalls sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,web_search_call_count FROM attempts`).Scan(&state, &usageStatus, &input, &output, &searchCalls); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted_unknown" || usageStatus != "unknown" || input.Valid || output.Valid || searchCalls.Valid {
		t.Fatalf("accounting=%s/%s input=%v output=%v search=%v", state, usageStatus, input, output, searchCalls)
	}
}

func TestOrdinaryAnthropicToolStreamingPreservesEventsAfterModelNormalization(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"weather\",\"input\":{}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	ctx, database, handler, secret, firstCalls, secondCalls := anthropicWebSearchStreamFixture(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, stream)
	})
	result := performAnthropicRequest(t, handler, secret, `{"model":"claude-search","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Weather"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]}`)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"model":"claude-search"`) || !strings.Contains(result.Body.String(), `"name":"weather"`) || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("response=%d first=%d second=%d body=%s", result.Code, firstCalls.Load(), secondCalls.Load(), result.Body.String())
	}
	var state, usageStatus, toolStatus string
	var input, output, tools int64
	if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,response_tool_call_count,tool_call_status FROM attempts`).Scan(&state, &usageStatus, &input, &output, &tools, &toolStatus); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || usageStatus != "provider_reported" || input != 3 || output != 1 || tools != 1 || toolStatus != "completed" {
		t.Fatalf("accounting=%s/%s input=%d output=%d tools=%d/%s", state, usageStatus, input, output, tools, toolStatus)
	}
}

func anthropicWebSearchSSE(stop, serverUsage, content string) string {
	usage := `{"input_tokens":7,"output_tokens":2}`
	if serverUsage != "" {
		usage = `{"input_tokens":7,"output_tokens":2,"server_tool_use":` + serverUsage + `}`
	}
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-upstream\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" + content + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stop + "\",\"stop_sequence\":null},\"usage\":" + usage + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func anthropicWebSearchStreamFixture(t *testing.T, serve http.HandlerFunc) (context.Context, *sql.DB, http.Handler, string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	firstCalls, secondCalls := new(atomic.Int64), new(atomic.Int64)
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		firstCalls.Add(1)
		serve(response, request)
	}))
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, anthropicWebSearchSSE("end_turn", `{"web_search_requests":0}`, ""))
	}))
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	t.Cleanup(func() { _ = store.Close() })
	firstConnection, firstUpstream, model := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1", "first", "claude-search")
	secondConnection, secondUpstream, _ := publishAnthropicWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, second.URL+"/v1", "second", "")
	model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stream", Scopes: []string{"chat:generate", "messages:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	return ctx, store.SystemDB(), mux, secret, firstCalls, secondCalls
}

func publishAnthropicWebSearchModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID, publicID string) (providers.Connection, providers.UpstreamModel, providers.PublicModel) {
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
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"chat", "web_search"})
	if err != nil {
		t.Fatal(err)
	}
	var model providers.PublicModel
	if publicID != "" {
		model, err = service.CreatePublicModel(ctx, owner, publicID, "Anthropic web search", "", upstream.ID, upstream.Capabilities)
		if err != nil {
			t.Fatal(err)
		}
	}
	return connection, upstream, model
}

func performAnthropicRequest(t *testing.T, handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func int64Pointer(value int64) *int64 { return &value }
