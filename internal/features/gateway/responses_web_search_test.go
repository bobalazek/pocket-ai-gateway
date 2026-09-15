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

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func TestResponseWebSearchValidation(t *testing.T) {
	valid := []string{
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"function","name":"local","parameters":{}},{"type":"web_search","search_context_size":"high","user_location":{"type":"approximate","city":"Ljubljana","country":"SI","region":null,"timezone":"Europe/Ljubljana"},"filters":{"allowed_domains":["example.com"],"blocked_domains":["ads.example"]},"external_web_access":true,"return_token_budget":"default"}],"max_tool_calls":4,"max_output_tokens":128,"include":["web_search_call.action.sources"],"stream":false}`,
	}
	for _, raw := range valid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		request, err := validateResponseWebSearch(envelope)
		if err != nil || !request.enabled || request.maxCalls < 1 {
			t.Fatalf("valid request rejected: %s: %#v, %v", raw, request, err)
		}
	}
	invalid := []string{
		`{"tools":[{"type":"web_search_preview"}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"},{"type":"web_search"}],"max_tool_calls":2,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":null,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":5,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":0}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"stream":true}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"conversation":"conv_1"}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"input":[{"type":"input_file","file_id":"file_1"}]}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"prompt":{"id":"pmpt_1"}}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"prompt_cache_key":"cache"}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"prompt_cache_options":{}}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"prompt_cache_retention":"24h"}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"input":[{"role":"user","content":[{"type":"input_text","text":"news","prompt_cache_breakpoint":{}}]}]}`,
		`{"tools":[{"type":"web_search","return_token_budget":null}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search","return_token_budget":"unlimited"}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search","search_content_types":["text","image"]}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search","filters":{"allowed_domains":["https://example.com"]}}],"max_tool_calls":1,"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"}],"max_tool_calls":1,"max_output_tokens":1,"include":["file_search_call.results"]}`,
	}
	for _, raw := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		if _, err := validateResponseWebSearch(envelope); err == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
}

func TestCompletedWebSearchCallsDeduplicatesSearchActions(t *testing.T) {
	for _, responseStatus := range []string{"completed", "incomplete"} {
		raw := []byte(`{"status":"` + responseStatus + `","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"one"}},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"one"}},{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://example.com"}},{"type":"web_search_call","id":"ws_3","status":"in_progress"},{"type":"web_search_call","id":"ws_4","status":"searching"},{"type":"web_search_call","id":"ws_5","status":"failed"},{"type":"web_search_call","id":"ws_6","status":"incomplete"}]}`)
		parsed, err := parseWebSearchResponse(raw)
		if err != nil || parsed.status != responseStatus || parsed.completedCalls != 1 {
			t.Fatalf("status=%s parsed=%#v err=%v", responseStatus, parsed, err)
		}
	}
	for _, invalid := range []string{
		`{"output":[{"type":"web_search_call","id":"ws_1","status":"completed"}]}`,
		`{"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"image_search"}}]}`,
	} {
		if _, err := parseWebSearchResponse([]byte(invalid)); err == nil {
			t.Fatalf("invalid provider output accepted: %s", invalid)
		}
	}
}

func TestWebSearchTargetEligibility(t *testing.T) {
	valid := providers.Target{PublicModel: providers.PublicModel{Adapter: "openai", Capabilities: []string{"chat", "web_search"}, RoutingStrategy: "fixed"}, UpstreamCapabilities: []string{"chat", "web_search"}, Preset: "openai"}
	if eligible, reason := webSearchTargetEligibility(valid); !eligible {
		t.Fatalf("valid target rejected: %s", reason)
	}
	tests := []struct {
		name   string
		mutate func(*providers.Target)
	}{
		{"custom preset", func(target *providers.Target) { target.Preset = "custom" }},
		{"compatible adapter", func(target *providers.Target) { target.Adapter = "openai_compatible" }},
		{"missing public capability", func(target *providers.Target) { target.Capabilities = []string{"chat"} }},
		{"missing upstream capability", func(target *providers.Target) { target.UpstreamCapabilities = []string{"chat"} }},
		{"free only", func(target *providers.Target) { target.FreeOnly = true }},
		{"lowest cost", func(target *providers.Target) { target.RoutingStrategy = "lowest_cost" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := valid
			test.mutate(&target)
			if eligible, _ := webSearchTargetEligibility(target); eligible {
				t.Fatalf("ineligible target accepted: %#v", target)
			}
		})
	}
}

func TestHostedWebSearchDetectionIsTypeSpecific(t *testing.T) {
	for _, raw := range []string{`[{"type":"web_search"}]`, `[{"type":"web_search_preview"}]`, `[{"type":"web_search_20250305"}]`} {
		if !containsHostedWebSearchTool(json.RawMessage(raw)) {
			t.Fatalf("hosted tool not detected: %s", raw)
		}
	}
	if containsHostedWebSearchTool(json.RawMessage(`[{"type":"function","name":"web_search"}]`)) {
		t.Fatal("function named web_search was treated as a hosted tool")
	}
}

func TestResponsesWebSearchScopeAndBackgroundRecheckPreventDispatch(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, upstreamModel, model := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "upstream", "assistant")
	mux := http.NewServeMux()
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	handler.Register(mux)
	requestBody := `{"model":"assistant","store":false,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`
	_, unscopedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Unscoped", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := performResponseRequest(t, mux, unscopedSecret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","store":false,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search","return_token_budget":null}]}`)
	if invalid.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("null return token budget status=%d calls=%d body=%s", invalid.Code, calls.Load(), invalid.Body.String())
	}
	denied := performResponseRequest(t, mux, unscopedSecret, http.MethodPost, "/api/openai/v1/responses", requestBody)
	if denied.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("unscoped status=%d calls=%d body=%s", denied.Code, calls.Load(), denied.Body.String())
	}
	key, scopedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Background", Scopes: []string{"responses:generate", "responses:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	queued := performResponseRequest(t, mux, scopedSecret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","background":true,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`)
	var state struct {
		ID string `json:"id"`
	}
	if queued.Code != http.StatusOK || json.Unmarshal(queued.Body.Bytes(), &state) != nil || state.ID == "" || calls.Load() != 0 {
		t.Fatalf("queue status=%d calls=%d body=%s", queued.Code, calls.Load(), queued.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["responses:generate"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	workerContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.RunBackground(workerContext)
	}()
	waitResponseState(t, store.SystemDB(), state.ID, "failed")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background worker did not stop")
	}
	if calls.Load() != 0 {
		t.Fatalf("background scope revocation dispatched %d calls", calls.Load())
	}

	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["responses:generate","responses:web_search"]' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	queued = performResponseRequest(t, mux, scopedSecret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","background":true,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`)
	if queued.Code != http.StatusOK || json.Unmarshal(queued.Body.Bytes(), &state) != nil || state.ID == "" {
		t.Fatalf("second queue status=%d body=%s", queued.Code, queued.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE upstream_models SET capabilities_json='["chat"]' WHERE id=?`, upstreamModel.ID); err != nil {
		t.Fatal(err)
	}
	workerContext, cancel = context.WithCancel(ctx)
	done = make(chan struct{})
	go func() {
		defer close(done)
		handler.RunBackground(workerContext)
	}()
	waitResponseState(t, store.SystemDB(), state.ID, "failed")
	cancel()
	<-done
	if calls.Load() != 0 {
		t.Fatalf("background capability revocation dispatched %d calls", calls.Load())
	}
}

func TestResponsesWebSearchPreservesTerminalFailureResponses(t *testing.T) {
	for _, terminalStatus := range []string{"failed", "cancelled"} {
		t.Run(terminalStatus, func(t *testing.T) {
			var primaryCalls, fallbackCalls atomic.Int64
			primary := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				primaryCalls.Add(1)
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, `{"id":"resp_terminal","object":"response","status":"`+terminalStatus+`","model":"upstream-one","output":[],"usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}`)
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				http.Error(response, "unexpected", http.StatusInternalServerError)
			}))
			defer fallback.Close()

			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			primaryConnection, primaryUpstream, model := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, primary.URL+"/v1", "upstream-one", "assistant")
			fallbackConnection, fallbackUpstream, _ := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, fallback.URL+"/v1", "upstream-two", "")
			model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: primaryUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: fallbackUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Web search terminal", Scopes: []string{"responses:generate", "responses:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			handler := New(store.SystemDB(), keyService, providerService, usageService)
			handler.Register(mux)
			body := `{"model":"assistant","input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`
			synchronous := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
			var synchronousResult struct {
				ID         string `json:"id"`
				Status     string `json:"status"`
				Model      string `json:"model"`
				Store      bool   `json:"store"`
				Background bool   `json:"background"`
			}
			if synchronous.Code != http.StatusOK || json.Unmarshal(synchronous.Body.Bytes(), &synchronousResult) != nil || synchronousResult.ID == "" || synchronousResult.ID == "resp_terminal" || synchronousResult.Status != terminalStatus || synchronousResult.Model != "assistant" || !synchronousResult.Store || synchronousResult.Background {
				t.Fatalf("synchronous status=%d body=%s", synchronous.Code, synchronous.Body.String())
			}
			retrieved := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+synchronousResult.ID, "")
			if retrieved.Code != http.StatusOK || retrieved.Body.String() != synchronous.Body.String() {
				t.Fatalf("retrieved status=%d body=%s want=%s", retrieved.Code, retrieved.Body.String(), synchronous.Body.String())
			}
			var synchronousState string
			if err := store.SystemDB().QueryRowContext(ctx, `SELECT state FROM stored_responses WHERE id=?`, synchronousResult.ID).Scan(&synchronousState); err != nil || synchronousState != terminalStatus {
				t.Fatalf("stored synchronous state=%s err=%v", synchronousState, err)
			}
			assertLatestWebSearchFailure(t, store.SystemDB())

			workerContext, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler.RunBackground(workerContext)
			}()
			background := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","background":true,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`)
			var queued struct {
				ID string `json:"id"`
			}
			if background.Code != http.StatusOK || json.Unmarshal(background.Body.Bytes(), &queued) != nil || queued.ID == "" {
				cancel()
				<-done
				t.Fatalf("background status=%d body=%s", background.Code, background.Body.String())
			}
			waitResponseState(t, store.SystemDB(), queued.ID, terminalStatus)
			cancel()
			<-done
			var storedBody []byte
			if err := store.SystemDB().QueryRowContext(ctx, `SELECT body_json FROM stored_responses WHERE id=?`, queued.ID).Scan(&storedBody); err != nil {
				t.Fatal(err)
			}
			var storedResult struct {
				ID         string `json:"id"`
				Status     string `json:"status"`
				Model      string `json:"model"`
				Store      bool   `json:"store"`
				Background bool   `json:"background"`
			}
			if json.Unmarshal(storedBody, &storedResult) != nil || storedResult.ID != queued.ID || storedResult.Status != terminalStatus || storedResult.Model != "assistant" || !storedResult.Store || !storedResult.Background {
				t.Fatalf("stored background body=%s", storedBody)
			}
			assertLatestWebSearchFailure(t, store.SystemDB())
			if primaryCalls.Load() != 2 || fallbackCalls.Load() != 0 {
				t.Fatalf("primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
			}
			var routeSuccesses, routeFailures int64
			if err := store.SystemDB().QueryRowContext(ctx, `SELECT success_count,failure_count FROM route_observations WHERE upstream_model_id=? AND operation='responses'`, primaryUpstream.ID).Scan(&routeSuccesses, &routeFailures); err != nil || routeSuccesses != 0 || routeFailures != 2 {
				t.Fatalf("route outcomes successes=%d failures=%d err=%v", routeSuccesses, routeFailures, err)
			}
		})
	}
}

func assertLatestWebSearchFailure(t *testing.T, database *sql.DB) {
	t.Helper()
	var state, usageStatus, requestState string
	var input, output, cost, calls sql.NullInt64
	if err := database.QueryRowContext(t.Context(), `SELECT attempts.state,attempts.usage_status,attempts.input_tokens,attempts.output_tokens,attempts.as_recorded_cost_nanos,attempts.web_search_call_count,requests.state FROM attempts JOIN requests ON requests.id=attempts.request_id ORDER BY attempts.rowid DESC LIMIT 1`).Scan(&state, &usageStatus, &input, &output, &cost, &calls, &requestState); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || usageStatus != "unknown" || requestState != "failed" || input.Valid || output.Valid || cost.Valid || calls.Valid {
		t.Fatalf("terminal accounting=%s/%s request=%s input=%v output=%v cost=%v calls=%v", state, usageStatus, requestState, input, output, cost, calls)
	}
}

func TestResponsesWebSearchNativeAccountingAndNoFallback(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int64
	var omitUsage atomic.Bool
	var forwarded map[string]json.RawMessage
	primary := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		primaryCalls.Add(1)
		_ = json.NewDecoder(request.Body).Decode(&forwarded)
		response.Header().Set("Content-Type", "application/json")
		if omitUsage.Load() {
			_, _ = io.WriteString(response, `{"id":"resp_upstream_unknown","object":"response","status":"completed","model":"upstream-one","output":[{"type":"web_search_call","id":"ws_unknown","status":"completed","action":{"type":"search","query":"news"}}]}`)
			return
		}
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","model":"upstream-one","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"news"}},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"news"}},{"type":"function_call","id":"fc_1","call_id":"call_1","status":"completed","name":"local","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		http.Error(response, "unexpected", http.StatusInternalServerError)
	}))
	defer fallback.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	primaryConnection, primaryUpstream, model := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, primary.URL+"/v1", "upstream-one", "assistant")
	fallbackConnection, fallbackUpstream, _ := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, fallback.URL+"/v1", "upstream-two", "")
	model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: primaryUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: fallbackUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Web search", Scopes: []string{"responses:generate", "responses:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	body := `{"model":"assistant","store":false,"stream":false,"input":"news","max_tool_calls":2,"max_output_tokens":64,"include":["web_search_call.action.sources"],"tools":[{"type":"web_search","return_token_budget":"default"},{"type":"function","name":"local","parameters":{"type":"object"}}]}`
	response := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
	if response.Code != http.StatusOK || primaryCalls.Load() != 1 || fallbackCalls.Load() != 0 {
		t.Fatalf("status=%d primary=%d fallback=%d body=%s", response.Code, primaryCalls.Load(), fallbackCalls.Load(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"model":"assistant"`) || strings.Contains(response.Body.String(), `"model":"upstream-one"`) {
		t.Fatalf("public model was not restored: %s", response.Body.String())
	}
	var upstreamModel string
	var stored bool
	if json.Unmarshal(forwarded["model"], &upstreamModel) != nil || json.Unmarshal(forwarded["store"], &stored) != nil || upstreamModel != "upstream-one" || stored {
		t.Fatalf("forwarded=%s", webSearchJSON(forwarded))
	}
	var requestTools, responseTools, maximumCalls, searchCalls, estimatedInput int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT request_tool_count,response_tool_call_count,web_search_max_calls,web_search_call_count,estimated_tokens FROM attempts`).Scan(&requestTools, &responseTools, &maximumCalls, &searchCalls, &estimatedInput); err != nil {
		t.Fatal(err)
	}
	if requestTools != 2 || responseTools != 2 || maximumCalls != 2 || searchCalls != 1 || estimatedInput < 2*webSearchInputPerCall {
		t.Fatalf("accounting request=%d response=%d max=%d search=%d estimate=%d", requestTools, responseTools, maximumCalls, searchCalls, estimatedInput)
	}
	omitUsage.Store(true)
	unknown := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
	if unknown.Code != http.StatusOK || fallbackCalls.Load() != 0 {
		t.Fatalf("unknown usage status=%d fallback=%d body=%s", unknown.Code, fallbackCalls.Load(), unknown.Body.String())
	}
	var unknownState, unknownStatus string
	var unknownInput, unknownOutput, unknownCost, unknownCalls sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,web_search_call_count FROM attempts WHERE usage_status='unknown'`).Scan(&unknownState, &unknownStatus, &unknownInput, &unknownOutput, &unknownCost, &unknownCalls); err != nil {
		t.Fatal(err)
	}
	if unknownState != "succeeded" || unknownStatus != "unknown" || unknownInput.Valid || unknownOutput.Valid || unknownCost.Valid || unknownCalls.Valid {
		t.Fatalf("unknown accounting=%s/%s input=%v output=%v cost=%v calls=%v", unknownState, unknownStatus, unknownInput, unknownOutput, unknownCost, unknownCalls)
	}
}

func TestResponsesWebSearchDoesNotFallbackAfterDispatch(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(response, "busy", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = io.WriteString(response, `{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer second.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, firstUpstream, model := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1", "first", "assistant")
	secondConnection, secondUpstream, _ := publishWebSearchModel(t, ctx, store.SystemDB(), providerService, owner, second.URL+"/v1", "second", "")
	model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Web search", Scopes: []string{"responses:generate", "responses:web_search"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	response := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","store":false,"input":"news","max_tool_calls":1,"max_output_tokens":8,"tools":[{"type":"web_search"}]}`)
	if response.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("status=%d first=%d second=%d body=%s", response.Code, firstCalls.Load(), secondCalls.Load(), response.Body.String())
	}
	var state, usageStatus string
	var input, output, cost, calls sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,web_search_call_count FROM attempts`).Scan(&state, &usageStatus, &input, &output, &cost, &calls); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || usageStatus != "unknown" || input.Valid || output.Valid || cost.Valid || calls.Valid {
		t.Fatalf("failed accounting=%s/%s input=%v output=%v cost=%v calls=%v", state, usageStatus, input, output, cost, calls)
	}
}

func publishWebSearchModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID, publicID string) (providers.Connection, providers.UpstreamModel, providers.PublicModel) {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: upstreamID, Preset: "openai", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"chat", "web_search"})
	if err != nil {
		t.Fatal(err)
	}
	var model providers.PublicModel
	if publicID != "" {
		model, err = service.CreatePublicModel(ctx, owner, publicID, "Web search", "", upstream.ID, []string{"chat", "web_search"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", baseURL, connection.ID); err != nil {
		t.Fatal(err)
	}
	return connection, upstream, model
}

func webSearchJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
