package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestGeminiInteractionsCreateForwardsPreservesAndAccounts(t *testing.T) {
	upstreamBodies := []string{
		`{"created":"2026-09-16T08:00:00Z","id":"int_completed","model":"gemini-3.8-flash","object":"interaction","service_tier":"standard","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"Hello."}]}],"updated":"2026-09-16T08:00:01Z","usage":{"total_cached_tokens":4,"total_input_tokens":10,"total_output_tokens":5,"total_thought_tokens":0,"total_tokens":15,"total_tool_use_tokens":0}}`,
		`{"created":"2026-09-16T08:01:00Z","id":"int_incomplete","model":"gemini-3.8-flash","object":"interaction","status":"incomplete","updated":"2026-09-16T08:01:01Z","usage":{"total_cached_tokens":0,"total_input_tokens":2,"total_output_tokens":1,"total_tokens":3}}`,
	}
	var forwarded [][]byte
	var paths, methods, credentials []string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		forwarded = append(forwarded, body)
		paths = append(paths, request.URL.Path)
		methods = append(methods, request.Method)
		credentials = append(credentials, request.Header.Get("x-goog-api-key"))
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, upstreamBodies[len(forwarded)-1])
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishGeminiInteractionModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1beta", "gemini-3.8-flash", "assistant")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Gemini interactions", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	cacheRate := "0.5"
	price, err := usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: model.ID, InputUSDPerMillion: "2", OutputUSDPerMillion: "4", CacheReadUSDPerMillion: &cacheRate, Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)

	requests := []string{
		`{"model":"assistant","input":"Hello"}`,
		`{"model":"assistant","input":"Continue","stream":false,"store":false,"background":false,"tools":[],"generation_config":{"max_output_tokens":64}}`,
	}
	for index, body := range requests {
		response := performGeminiInteraction(t, mux, secret, body)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", index, response.Code, response.Body.String())
		}
		var got, want map[string]any
		if json.Unmarshal(response.Body.Bytes(), &got) != nil || json.Unmarshal([]byte(upstreamBodies[index]), &want) != nil {
			t.Fatalf("request %d invalid response=%s", index, response.Body.String())
		}
		want["model"] = model.ID
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request %d response=%#v want=%#v", index, got, want)
		}
	}
	if !reflect.DeepEqual(paths, []string{"/v1beta/interactions", "/v1beta/interactions"}) || !reflect.DeepEqual(methods, []string{http.MethodPost, http.MethodPost}) || !reflect.DeepEqual(credentials, []string{"provider-secret", "provider-secret"}) {
		t.Fatalf("upstream paths=%v methods=%v credentials=%v", paths, methods, credentials)
	}
	for index, raw := range forwarded {
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["model"] != "gemini-3.8-flash" || body["store"] != false {
			t.Fatalf("forwarded %d=%s", index, raw)
		}
		if index == 0 && body["input"] != "Hello" || index == 1 && body["input"] != "Continue" {
			t.Fatalf("forwarded %d input=%v", index, body["input"])
		}
	}

	rows, err := store.SystemDB().QueryContext(ctx, `SELECT attempts.state,attempts.usage_status,attempts.input_tokens,attempts.output_tokens,attempts.cache_read_input_tokens,attempts.as_recorded_cost_nanos,attempts.price_version_id,attempts.target_dialect,attempts.target_operation,requests.operation,requests.dialect
		FROM attempts JOIN requests ON requests.id=attempts.request_id ORDER BY attempts.rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantAccounting := []struct{ input, output, cache, cost int64 }{{10, 5, 4, 34_000}, {2, 1, 0, 8_000}}
	for index, want := range wantAccounting {
		if !rows.Next() {
			t.Fatalf("missing accounting row %d", index)
		}
		var state, usageStatus, priceID, targetDialect, targetOperation, operation, dialect string
		var input, output, cache, cost int64
		if err := rows.Scan(&state, &usageStatus, &input, &output, &cache, &cost, &priceID, &targetDialect, &targetOperation, &operation, &dialect); err != nil {
			t.Fatal(err)
		}
		if state != "succeeded" || usageStatus != "provider_reported" || input != want.input || output != want.output || cache != want.cache || cost != want.cost || priceID != price.ID || targetDialect != "gemini" || targetOperation != "interactions" || operation != "interactions" || dialect != "gemini" {
			t.Fatalf("row %d state=%s usage=%s tokens=%d/%d/%d cost=%d price=%s target=%s/%s request=%s/%s", index, state, usageStatus, input, output, cache, cost, priceID, targetDialect, targetOperation, operation, dialect)
		}
	}
	if rows.Next() {
		t.Fatal("unexpected extra accounting row")
	}
}

func TestGeminiInteractionsRejectUnsupportedRequestsBeforeDispatch(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _, model := publishGeminiInteractionModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1beta", "gemini-3.8-flash", "assistant")
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Gemini interactions", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, unscoped, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No generation", Scopes: []string{"models:read"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)

	if response := performGeminiInteraction(t, mux, "", `{"model":"assistant","input":"Hello"}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing x-goog-api-key status=%d body=%s", response.Code, response.Body.String())
	} else {
		assertGeminiErrorStatus(t, response, "UNAUTHENTICATED")
	}
	if response := performGeminiInteraction(t, mux, unscoped, `{"model":"assistant","input":"Hello"}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing chat scope status=%d body=%s", response.Code, response.Body.String())
	} else {
		assertGeminiErrorStatus(t, response, "NOT_FOUND")
	}
	invalid := []struct{ name, body string }{
		{"invalid JSON", `{`},
		{"missing model", `{"input":"Hello"}`},
		{"non-string model", `{"model":1,"input":"Hello"}`},
		{"missing input", `{"model":"assistant"}`},
		{"empty input", `{"model":"assistant","input":""}`},
		{"null input", `{"model":"assistant","input":null}`},
		{"array input", `{"model":"assistant","input":[]}`},
		{"stream true", `{"model":"assistant","input":"Hello","stream":true}`},
		{"stream wrong type", `{"model":"assistant","input":"Hello","stream":"false"}`},
		{"store true", `{"model":"assistant","input":"Hello","store":true}`},
		{"store wrong type", `{"model":"assistant","input":"Hello","store":"false"}`},
		{"background true", `{"model":"assistant","input":"Hello","background":true}`},
		{"background wrong type", `{"model":"assistant","input":"Hello","background":"false"}`},
		{"previous interaction", `{"model":"assistant","input":"Hello","previous_interaction_id":"int_previous"}`},
		{"agent", `{"model":"assistant","input":"Hello","agent":"deep-research"}`},
		{"agent config", `{"model":"assistant","input":"Hello","agent_config":{}}`},
		{"environment", `{"model":"assistant","input":"Hello","environment":{}}`},
		{"webhook config", `{"model":"assistant","input":"Hello","webhook_config":{}}`},
		{"tools", `{"model":"assistant","input":"Hello","tools":[{"type":"google_search"}]}`},
		{"generation config type", `{"model":"assistant","input":"Hello","generation_config":"bad"}`},
		{"generation output bound", `{"model":"assistant","input":"Hello","generation_config":{"max_output_tokens":0}}`},
		{"generation config field", `{"model":"assistant","input":"Hello","generation_config":{"temperature":0.2}}`},
		{"response format", `{"model":"assistant","input":"Hello","response_format":{"type":"json_schema"}}`},
		{"response modalities", `{"model":"assistant","input":"Hello","response_modalities":["text"]}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			response := performGeminiInteraction(t, mux, secret, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertGeminiErrorStatus(t, response, "INVALID_ARGUMENT")
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected requests dispatched %d upstream calls", calls.Load())
	}
}

func TestGeminiInteractionsRequireNativeBuiltInGeminiTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, context.Context, *sql.DB, providers.Connection, providers.UpstreamModel, providers.PublicModel)
	}{
		{"custom preset", func(t *testing.T, ctx context.Context, database *sql.DB, connection providers.Connection, _ providers.UpstreamModel, _ providers.PublicModel) {
			if _, err := database.ExecContext(ctx, "UPDATE provider_connections SET preset='custom' WHERE id=?", connection.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"translated adapter", func(t *testing.T, ctx context.Context, database *sql.DB, connection providers.Connection, _ providers.UpstreamModel, _ providers.PublicModel) {
			if _, err := database.ExecContext(ctx, "UPDATE provider_connections SET adapter='openai',preset='openai' WHERE id=?", connection.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing public capability", func(t *testing.T, ctx context.Context, database *sql.DB, _ providers.Connection, _ providers.UpstreamModel, model providers.PublicModel) {
			if _, err := database.ExecContext(ctx, `UPDATE public_models SET capabilities_json='["chat"]' WHERE id=?`, model.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing upstream capability", func(t *testing.T, ctx context.Context, database *sql.DB, _ providers.Connection, upstream providers.UpstreamModel, _ providers.PublicModel) {
			if _, err := database.ExecContext(ctx, `UPDATE upstream_models SET capabilities_json='["chat"]' WHERE id=?`, upstream.ID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(response, `{}`)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, upstreamModel, model := publishGeminiInteractionModel(t, ctx, store.SystemDB(), providerService, owner, upstream.URL+"/v1beta", "gemini-3.8-flash", "assistant")
			test.mutate(t, ctx, store.SystemDB(), connection, upstreamModel, model)
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Gemini interactions", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			response := performGeminiInteraction(t, mux, secret, `{"model":"assistant","input":"Hello"}`)
			var attempts int64
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts").Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusNotFound || calls.Load() != 0 || attempts != 0 {
				t.Fatalf("status=%d calls=%d attempts=%d body=%s", response.Code, calls.Load(), attempts, response.Body.String())
			}
			assertGeminiErrorStatus(t, response, "NOT_FOUND")
		})
	}
}

func TestGeminiInteractionsDoNotFallbackAfterMalformedOrFailedTerminalResponse(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"malformed", `{"id":"upstream-only"`},
		{"failed", `{"id":"upstream-only","model":"first","object":"interaction","status":"failed","usage":{"total_cached_tokens":0,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1}}`},
		{"null usage", `{"id":"upstream-only","model":"first","object":"interaction","status":"completed","steps":[],"usage":{"total_cached_tokens":0,"total_input_tokens":null,"total_output_tokens":0,"total_tokens":0}}`},
		{"tool usage", `{"id":"upstream-only","model":"first","object":"interaction","status":"completed","steps":[],"usage":{"total_cached_tokens":0,"total_input_tokens":1,"total_output_tokens":1,"total_tool_use_tokens":1,"total_tokens":3}}`},
		{"unexplained usage", `{"id":"upstream-only","model":"first","object":"interaction","status":"completed","steps":[],"usage":{"total_cached_tokens":0,"total_input_tokens":1,"total_output_tokens":1,"total_thought_tokens":0,"total_tokens":3}}`},
		{"non-text output", `{"id":"upstream-only","model":"first","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"type":"image","data":"opaque"}]}],"usage":{"total_cached_tokens":0,"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, test.body)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				secondCalls.Add(1)
				_, _ = io.WriteString(response, `{"id":"unexpected","model":"second","object":"interaction","status":"completed","steps":[],"usage":{"total_cached_tokens":0,"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}`)
			}))
			defer second.Close()

			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			firstConnection, firstUpstream, model := publishGeminiInteractionModel(t, ctx, store.SystemDB(), providerService, owner, first.URL+"/v1beta", "first", "assistant")
			secondConnection, secondUpstream, _ := publishGeminiInteractionModel(t, ctx, store.SystemDB(), providerService, owner, second.URL+"/v1beta", "second", "")
			model, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstUpstream.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondUpstream.ID, Priority: 2, Weight: 1, Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No interaction fallback", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			response := performGeminiInteraction(t, mux, secret, `{"model":"assistant","input":"Hello"}`)
			if response.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 || strings.Contains(response.Body.String(), "upstream-only") {
				t.Fatalf("status=%d first=%d second=%d body=%s", response.Code, firstCalls.Load(), secondCalls.Load(), response.Body.String())
			}
			assertGeminiErrorStatus(t, response, "UNAVAILABLE")
			var state, usageStatus string
			var input, output, cache sql.NullInt64
			var attempts int64
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*),state,usage_status,input_tokens,output_tokens,cache_read_input_tokens FROM attempts").Scan(&attempts, &state, &usageStatus, &input, &output, &cache); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || state != "failed" || usageStatus != "unknown" || input.Valid || output.Valid || cache.Valid {
				t.Fatalf("attempts=%d state=%s usage=%s input=%v output=%v cache=%v", attempts, state, usageStatus, input, output, cache)
			}
		})
	}
}

func publishGeminiInteractionModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID, publicID string) (providers.Connection, providers.UpstreamModel, providers.PublicModel) {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: upstreamID, Preset: "gemini", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", baseURL, connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"chat", "interactions"})
	if err != nil {
		t.Fatal(err)
	}
	var model providers.PublicModel
	if publicID != "" {
		model, err = service.CreatePublicModel(ctx, owner, publicID, "Gemini interactions", "", upstream.ID, upstream.Capabilities)
		if err != nil {
			t.Fatal(err)
		}
	}
	return connection, upstream, model
}

func performGeminiInteraction(t *testing.T, handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/gemini/v1beta/interactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if secret != "" {
		request.Header.Set("x-goog-api-key", secret)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertGeminiErrorStatus(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Error.Status != want {
		t.Fatalf("Gemini error status=%q want=%q body=%s", envelope.Error.Status, want, response.Body.String())
	}
}
