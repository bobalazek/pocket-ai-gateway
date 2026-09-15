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
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestAnthropicPromptCacheAccountsAndProjectsBreakdown(t *testing.T) {
	const secretPrompt = "private cached prompt"
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(secretPrompt)) {
			t.Errorf("upstream body=%s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"id":"msg_cache","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5},"output_tokens":4}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishAnthropicCacheModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "claude-test")
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "cache", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 10_000}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: model.ID, InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	body := `{"model":"` + model.ID + `","max_tokens":8,"system":[{"type":"text","text":"` + secretPrompt + `","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"Hi"}]}`
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.StatusCode, calls, raw)
	}
	var input, output, creation, read, five, one int64
	var price, cost sql.NullString
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT input_tokens,output_tokens,cache_creation_input_tokens,cache_read_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens,price_version_id,as_recorded_cost_nanos FROM attempts`).Scan(&input, &output, &creation, &read, &five, &one, &price, &cost); err != nil {
		t.Fatal(err)
	}
	if input != 60 || output != 4 || creation != 20 || read != 30 || five != 15 || one != 5 || !price.Valid || cost.Valid {
		t.Fatalf("usage=%d/%d cache=%d/%d/%d/%d price=%v cost=%v", input, output, creation, read, five, one, price, cost)
	}
	var payload string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT payload_json FROM event_outbox WHERE event_type='attempt.settled'").Scan(&payload); err != nil || strings.Contains(payload, secretPrompt) {
		t.Fatalf("outbox contains prompt: %q err=%v", payload, err)
	}
	if _, err := usage.ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	if err := store.DataDB().QueryRowContext(ctx, `SELECT input_tokens,cache_creation_input_tokens,cache_read_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens FROM usage_daily`).Scan(&input, &creation, &read, &five, &one); err != nil || input != 60 || creation != 20 || read != 30 || five != 15 || one != 5 {
		t.Fatalf("projection=%d/%d/%d/%d/%d err=%v", input, creation, read, five, one, err)
	}
	summary, err := usageService.Summary(ctx, owner, usage.UsageQuery{})
	if err != nil || summary.InputTokens != 60 || summary.CacheCreationInputTokens != 20 || summary.CacheReadInputTokens != 30 || summary.CacheCreation5mTokens != 15 || summary.CacheCreation1hTokens != 5 || summary.KnownCostUSD != "0" || summary.UnknownAttempts != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

func TestAnthropicPromptCacheSSEUsageTotalsAllInputClasses(t *testing.T) {
	raw := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"cache_creation_input_tokens\":11,\"cache_read_input_tokens\":13,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3,\"ephemeral_1h_input_tokens\":8},\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
	parsed := parseUsageDetails("anthropic", raw)
	if parsed.inputTokens == nil || *parsed.inputTokens != 31 || parsed.outputTokens == nil || *parsed.outputTokens != 5 || parsed.cacheCreationInputTokens == nil || *parsed.cacheCreationInputTokens != 11 || parsed.cacheReadInputTokens == nil || *parsed.cacheReadInputTokens != 13 || parsed.cacheCreation5mTokens == nil || *parsed.cacheCreation5mTokens != 3 || parsed.cacheCreation1hTokens == nil || *parsed.cacheCreation1hTokens != 8 {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestAnthropicPromptCacheSSEDetailWithoutAggregateIsUnknown(t *testing.T) {
	raw := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":13,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3,\"ephemeral_1h_input_tokens\":8},\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
	if parsed := parseUsageDetails("anthropic", raw); parsed.inputTokens != nil || parsed.outputTokens != nil || parsed.cacheCreationInputTokens != nil || parsed.cacheReadInputTokens != nil || parsed.cacheCreation5mTokens != nil || parsed.cacheCreation1hTokens != nil {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestAnthropicPromptCacheValidation(t *testing.T) {
	valid := []string{
		`{"cache_control":{"type":"ephemeral"}}`,
		`{"cache_control":null,"tools":[{"cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"cache_control":{"type":"ephemeral","ttl":"5m"}}]}`,
		`{"tools":[{"input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}}}],"messages":[]}`,
	}
	for _, raw := range valid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		if _, err := validatePromptCache(envelope); err != nil {
			t.Fatalf("valid %s: %v", raw, err)
		}
	}
	invalid := []string{
		`{"cache_control":{"type":"persistent"}}`,
		`{"cache_control":{"type":"ephemeral","ttl":"2h"}}`,
		`{"cache_control":{"type":"ephemeral","unknown":true}}`,
		`{"cache_control":{"type":"ephemeral","ttl":null}}`,
		`{"system":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral","unknown":true}}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral","ttl":null}}]}]}`,
		`{"tools":[{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}}]}`,
		`{"system":[{"cache_control":{"type":"ephemeral","ttl":"5m"}},{"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
	}
	for _, raw := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &envelope)
		if _, err := validatePromptCache(envelope); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestAnthropicPromptCacheAutomaticBreakpointUsesLastCacheableBlock(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "four explicit plus trailing content string consumes fifth slot",
			body:    `{"cache_control":{"type":"ephemeral"},"tools":[{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}},{"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"trailing text"}]}`,
			wantErr: true,
		},
		{
			name: "matching control on last block is a no-op",
			body: `{"cache_control":{"type":"ephemeral","ttl":"5m"},"messages":[{"role":"user","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"three","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"four","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`,
		},
		{
			name:    "conflicting control on last block is rejected",
			body:    `{"cache_control":{"type":"ephemeral","ttl":"5m"},"messages":[{"role":"user","content":[{"type":"text","text":"last","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
			wantErr: true,
		},
		{
			name: "trailing non-cacheable block does not replace last cacheable block",
			body: `{"cache_control":{"type":"ephemeral","ttl":"5m"},"messages":[{"role":"assistant","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"three","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"four","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"thinking","thinking":"not a breakpoint target"},{"type":"text","text":""},{"type":"citation","cited_text":"nested only"}]}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.body), &envelope); err != nil {
				t.Fatal(err)
			}
			_, err := validatePromptCache(envelope)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%t", err, test.wantErr)
			}
		})
	}
}

func TestAnthropicPromptCacheRequiresNativeCapableRouteAndNeverFallsBack(t *testing.T) {
	var firstCalls, secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		firstCalls++
		http.Error(response, "busy", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		secondCalls++
		_, _ = io.WriteString(response, `{"usage":{"input_tokens":1,"cache_creation_input_tokens":1,"cache_read_input_tokens":0,"output_tokens":1}}`)
	}))
	defer second.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, public := publishAnthropicCacheModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1", "first")
	secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "second", Preset: "anthropic", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", second.URL+"/v1", secondConnection.ID); err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "second", []string{"chat", "prompt_cache"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, public.ID, public.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: public.TargetModelID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Weight: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "cache", Scopes: []string{"chat:generate"}, ModelPatterns: []string{public.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+public.ID+`","max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("dispatch calls=%d/%d", firstCalls, secondCalls)
	}
	var state, accounting string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status FROM attempts").Scan(&state, &accounting); err != nil || state != "failed" || accounting != "unknown" {
		t.Fatalf("attempt=%s/%s err=%v", state, accounting, err)
	}
}

func TestAnthropicPromptCacheMissingUsageBreakdownSettlesUnknown(t *testing.T) {
	for _, usageJSON := range []string{
		`{"input_tokens":7,"cache_read_input_tokens":3,"output_tokens":1}`,
		`{"input_tokens":7,"cache_creation_input_tokens":null,"cache_read_input_tokens":3,"output_tokens":1}`,
		`{"input_tokens":7,"cache_read_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1},"output_tokens":1}`,
		`{"input_tokens":7,"cache_creation_input_tokens":null,"cache_read_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1},"output_tokens":1}`,
		`{"input_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":null,"output_tokens":1}`,
	} {
		t.Run(usageJSON, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(response, `{"id":"msg","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":`+usageJSON+`}`)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishAnthropicCacheModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "missing")
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "cache", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+model.ID+`","max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"Hi"}]}`))
			request.Header.Set("x-api-key", secret)
			request.Header.Set("anthropic-version", "2023-06-01")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			var status string
			var input, output sql.NullInt64
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens FROM attempts").Scan(&status, &input, &output); err != nil || status != "unknown" || input.Valid || output.Valid {
				t.Fatalf("usage=%s input=%v output=%v err=%v", status, input, output, err)
			}
		})
	}
}

func TestAnthropicPromptCacheRejectsUnpricedRoutingBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		strategy string
		freeOnly bool
	}{{"lowest cost", "lowest_cost", false}, {"free only", "fixed", true}} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishAnthropicCacheModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1", "unpriced")
			if _, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: test.strategy, FreeOnly: test.freeOnly, Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Weight: 1, Enabled: true}}}); err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "cache", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+model.ID+`","max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"Hi"}]}`))
			request.Header.Set("x-api-key", secret)
			request.Header.Set("anthropic-version", "2023-06-01")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			var attempts int
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts").Scan(&attempts); err != nil || calls != 0 || attempts != 0 {
				t.Fatalf("calls=%d attempts=%d err=%v", calls, attempts, err)
			}
		})
	}
}

func TestAnthropicPromptCacheRejectsTranslatedTargetBeforeAdmission(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "gpt-test", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "cache", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+model.ID+`","max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var attempts int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts").Scan(&attempts); err != nil || calls != 0 || attempts != 0 || response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d calls=%d attempts=%d err=%v", response.StatusCode, calls, attempts, err)
	}
}

func publishAnthropicCacheModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID string) (providers.Connection, providers.PublicModel) {
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
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"chat", "prompt_cache"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.CreatePublicModel(ctx, owner, upstreamID+"-public", upstreamID, "", upstream.ID, upstream.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	return connection, model
}
