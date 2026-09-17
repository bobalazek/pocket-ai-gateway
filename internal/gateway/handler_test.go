package gateway

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestProviderCredentialHeaders(t *testing.T) {
	tests := []struct {
		adapter, preset, header, want string
		bearer                        bool
	}{
		{adapter: "openai", preset: "openai", header: "Authorization", want: "Bearer secret"},
		{adapter: "anthropic", preset: "anthropic", header: "x-api-key", want: "secret"},
		{adapter: "gemini", preset: "gemini", header: "x-goog-api-key", want: "secret"},
		{adapter: "openai_compatible", preset: "azure-openai", header: "api-key", want: "secret"},
		{adapter: "openai_compatible", preset: "azure-openai", header: "Authorization", want: "Bearer secret", bearer: true},
	}
	for _, test := range tests {
		request, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
		setProviderCredential(request, test.adapter, test.preset, "secret", test.bearer)
		if value := request.Header.Get(test.header); value != test.want {
			t.Errorf("%s/%s %s=%q", test.adapter, test.preset, test.header, value)
		}
		if test.bearer && request.Header.Get("api-key") != "" {
			t.Errorf("%s/%s bearer credential also set api-key", test.adapter, test.preset)
		}
	}
}

func TestNativeOpenAIForwardingUsesProviderCredentialAndAccounts(t *testing.T) {
	var gotAuthorization, gotModel string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuthorization = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "gpt-upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Client", Scopes: []string{"chat:generate", "models:read"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil || key.ID == "" {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(string(body), `"content":"Hello"`) {
		t.Fatalf("response %d: %s", response.StatusCode, body)
	}
	if gotAuthorization != "Bearer provider-secret" || gotModel != "gpt-upstream" {
		t.Fatalf("upstream auth/model = %q/%q", gotAuthorization, gotModel)
	}
	blocked, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"Search"}],"web_search_options":{}}`))
	blocked.Header.Set("Authorization", "Bearer "+secret)
	blocked.Header.Set("Content-Type", "application/json")
	blockedResponse, err := http.DefaultClient.Do(blocked)
	if err != nil {
		t.Fatal(err)
	}
	defer blockedResponse.Body.Close()
	if blockedResponse.StatusCode != http.StatusBadRequest || calls != 1 {
		t.Fatalf("web search status=%d upstream calls=%d", blockedResponse.StatusCode, calls)
	}
	var state, status, upstreamID string
	var input, output int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,upstream_model_id,input_tokens,output_tokens FROM attempts WHERE model_id='assistant'").Scan(&state, &status, &upstreamID, &input, &output); err != nil || state != "succeeded" || status != "provider_reported" || upstreamID != "gpt-upstream" || input != 4 || output != 2 {
		t.Fatalf("attempt = %s/%s %d/%d, %v", state, status, input, output, err)
	}
	_ = model
}

func TestNativeOpenAIModerationsUseScopedModel(t *testing.T) {
	var path, authorization, model string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		if body["input"] == "invalid" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"invalid moderation input","type":"invalid_request_error","code":"bad_input","param":"input","unsafe":"discard"}}`)
			return
		}
		if body["input"] == "limited" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit","param":null,"unsafe":"discard"}}`)
			return
		}
		io.WriteString(w, `{"id":"modr_1","model":"omni-moderation-latest","results":[{"flagged":true,"categories":{"violence":true},"category_scores":{"violence":0.9}}]}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "moderation-upstream", []string{"moderations"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Moderation", Scopes: []string{"moderations:classify"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 10_000}); err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No moderation", Scopes: []string{"chat:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	requestModeration := func(key, payload string) (int, []byte) {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/moderations", strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		responseBody, _ := io.ReadAll(response.Body)
		return response.StatusCode, responseBody
	}
	status, body := requestModeration(secret, `{"model":"`+publicModel.ID+`","input":["violent text"]}`)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"flagged":true`)) || !bytes.Contains(body, []byte(`"model":"`+publicModel.ID+`"`)) || bytes.Contains(body, []byte("omni-moderation-latest")) || path != "/v1/moderations" || authorization != "Bearer provider-secret" || model != "moderation-upstream" {
		t.Fatalf("status=%d body=%s upstream=%s %s %s", status, body, path, authorization, model)
	}
	var usageStatus, reservationState string
	var inputTokens, outputTokens int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT attempts.usage_status,attempts.input_tokens,attempts.output_tokens,reservations.state FROM attempts JOIN reservations ON reservations.attempt_id=attempts.id WHERE attempts.state='succeeded'`).Scan(&usageStatus, &inputTokens, &outputTokens, &reservationState); err != nil || usageStatus != "estimated" || inputTokens <= 0 || outputTokens != 0 || reservationState != "settled" {
		t.Fatalf("usage=%s %d/%d reservation=%s err=%v", usageStatus, inputTokens, outputTokens, reservationState, err)
	}
	for _, invalid := range []string{
		`{"model":"` + publicModel.ID + `"}`,
		`{"model":"` + publicModel.ID + `","input":null}`,
		`{"model":"` + publicModel.ID + `","input":[]}`,
		`{"model":"` + publicModel.ID + `","input":42}`,
		`{"model":"` + publicModel.ID + `","input":[42]}`,
		`{"model":"` + publicModel.ID + `","input":[null]}`,
		`{"model":"` + publicModel.ID + `","input":[{}]}`,
		`{"model":"` + publicModel.ID + `","input":["text",{"type":"text","text":"more"}]}`,
		`{"model":"` + publicModel.ID + `","input":[{"type":"image_url","image_url":{}}]}`,
		`{"model":"` + publicModel.ID + `","input":"text","stream":true}`,
	} {
		if status, _ = requestModeration(secret, invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid status=%d body=%s", status, invalid)
		}
	}
	for _, providerError := range []struct {
		input  string
		status int
		text   string
	}{{"invalid", http.StatusBadRequest, "invalid moderation input"}, {"limited", http.StatusTooManyRequests, "slow down"}} {
		status, body = requestModeration(secret, `{"model":"`+publicModel.ID+`","input":"`+providerError.input+`"}`)
		if status != providerError.status || !bytes.Contains(body, []byte(providerError.text)) || bytes.Contains(body, []byte("unsafe")) {
			t.Fatalf("provider error status=%d body=%s", status, body)
		}
	}
	status, _ = requestModeration(denied, `{"model":"`+publicModel.ID+`","input":"text"}`)
	if status != http.StatusNotFound || calls != 3 {
		t.Fatalf("denied status=%d upstream calls=%d", status, calls)
	}
}

func TestModerationModelRewriteDoesNotExpandHTML(t *testing.T) {
	rewritten, err := rewriteResponseModel([]byte(`{"model":"upstream","detail":"<&>"}`), "public")
	if err != nil || !bytes.Contains(rewritten, []byte(`"model":"public"`)) || !bytes.Contains(rewritten, []byte(`"detail":"<&>"`)) {
		t.Fatalf("rewritten=%s err=%v", rewritten, err)
	}
}

func TestNativeOpenAIResponseInputTokensUseScopedModel(t *testing.T) {
	var path, authorization, model string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		switch body["input"] {
		case "bad_missing":
			io.WriteString(w, `{}`)
			return
		case "bad_object":
			io.WriteString(w, `{"object":"wrong","input_tokens":12}`)
			return
		case "bad_negative":
			io.WriteString(w, `{"object":"response.input_tokens","input_tokens":-1}`)
			return
		case "bad_null":
			io.WriteString(w, `{"object":"response.input_tokens","input_tokens":null}`)
			return
		case "bad_fraction":
			io.WriteString(w, `{"object":"response.input_tokens","input_tokens":1.5}`)
			return
		case "bad_overflow":
			io.WriteString(w, `{"object":"response.input_tokens","input_tokens":9223372036854775808}`)
			return
		case "bad_unsafe":
			io.WriteString(w, `{"object":"response.input_tokens","input_tokens":9007199254740992}`)
			return
		}
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":12}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "count-upstream", []string{"count_tokens"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Count", Scopes: []string{"tokens:count"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No count", Scopes: []string{"chat:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	requestCount := func(key, payload string) (int, []byte) {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses/input_tokens", strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, body
	}
	status, body := requestCount(secret, `{"model":"`+publicModel.ID+`","input":"hello"}`)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"input_tokens":12`)) || path != "/v1/responses/input_tokens" || authorization != "Bearer provider-secret" || model != "count-upstream" {
		t.Fatalf("status=%d body=%s upstream=%s %s %s", status, body, path, authorization, model)
	}
	var usageStatus string
	var inputTokens, outputTokens, cost int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens,as_recorded_cost_nanos FROM attempts WHERE state='succeeded'").Scan(&usageStatus, &inputTokens, &outputTokens, &cost); err != nil || usageStatus != "provider_reported" || inputTokens != 12 || outputTokens != 0 || cost != 0 {
		t.Fatalf("usage=%s %d/%d cost=%d err=%v", usageStatus, inputTokens, outputTokens, cost, err)
	}
	status, body = requestCount(secret, `{"model":"`+publicModel.ID+`","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"file_id":{"type":"string"},"id":{"type":"string"}}}}]}`)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"input_tokens":12`)) {
		t.Fatalf("function schema status=%d body=%s", status, body)
	}
	for _, malformed := range []string{"bad_missing", "bad_object", "bad_negative", "bad_null", "bad_fraction", "bad_overflow", "bad_unsafe"} {
		status, body = requestCount(secret, `{"model":"`+publicModel.ID+`","input":"`+malformed+`"}`)
		if status != http.StatusBadGateway {
			t.Fatalf("malformed %s status=%d body=%s", malformed, status, body)
		}
		if _, err := store.SystemDB().ExecContext(ctx, "UPDATE route_observations SET consecutive_failures=0,circuit_open_until=NULL"); err != nil {
			t.Fatal(err)
		}
	}
	var failed int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 7 {
		t.Fatalf("failed attempts=%d err=%v", failed, err)
	}
	for _, invalid := range []string{
		`{"model":"` + publicModel.ID + `","input":42}`,
		`{"model":"` + publicModel.ID + `","input":[{"type":"item_reference","id":"item_1"}]}`,
		`{"model":"` + publicModel.ID + `","input":[{"id":"item_1"}]}`,
		`{"model":"` + publicModel.ID + `","input":[{"type":null,"id":"item_1"}]}`,
		`{"model":"` + publicModel.ID + `","input":"hello","conversation":"conv_1"}`,
		`{"model":"` + publicModel.ID + `","input":"hello","tools":[{"type":"web_search"}]}`,
	} {
		if status, _ = requestCount(secret, invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid status=%d body=%s", status, invalid)
		}
	}
	status, _ = requestCount(denied, `{"model":"`+publicModel.ID+`","input":"hello"}`)
	if status != http.StatusNotFound || calls != 9 {
		t.Fatalf("denied status=%d upstream calls=%d", status, calls)
	}
}

func TestInputTokenSemanticResponseDoesNotFallback(t *testing.T) {
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":null}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":12}`)
	}))
	defer fallback.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	primaryConnection, publicModel := publishModel(t, ctx, providerService, owner, "openai", primary.URL+"/v1", "primary", []string{"count_tokens"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback", []string{"count_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Count", Scopes: []string{"tokens:count"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses/input_tokens", strings.NewReader(`{"model":"`+publicModel.ID+`","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || primaryCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("status=%d primary=%d fallback=%d", response.StatusCode, primaryCalls, fallbackCalls)
	}
}

func TestInputTokenCountingRespectsPublishedCapabilities(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":12}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "openai", Adapter: "openai", BaseURL: upstream.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "multi-capable", []string{"chat", "count_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	publicModel, err := providerService.CreatePublicModel(ctx, owner, "chat-only", "Chat only", "", upstreamModel.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Count", Scopes: []string{"tokens:count"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses/input_tokens", strings.NewReader(`{"model":"`+publicModel.ID+`","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || calls != 0 {
		t.Fatalf("status=%d upstream calls=%d", response.StatusCode, calls)
	}
}

func TestNativeOpenAIImageGenerationUsesScopedModel(t *testing.T) {
	var path, authorization, model string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		io.WriteString(w, `{"created":1764967971,"data":[{"b64_json":"eA=="}],"usage":{"input_tokens":5,"input_tokens_details":{"image_tokens":0,"text_tokens":5},"output_tokens":7,"total_tokens":12}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "image-upstream", []string{"images"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Images", Scopes: []string{"images:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No images", Scopes: []string{"chat:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	generate := func(key, payload string) (int, []byte) {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/images/generations", strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, body
	}
	status, body := generate(secret, `{"model":"`+publicModel.ID+`","prompt":"A black dot","n":2}`)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"b64_json":"eA=="`)) || path != "/v1/images/generations" || authorization != "Bearer provider-secret" || model != "image-upstream" {
		t.Fatalf("status=%d body=%s upstream=%s %s %s", status, body, path, authorization, model)
	}
	var inputTokens, outputTokens int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT input_tokens,output_tokens FROM attempts WHERE state='succeeded'").Scan(&inputTokens, &outputTokens); err != nil || inputTokens != 5 || outputTokens != 7 {
		t.Fatalf("usage=%d/%d err=%v", inputTokens, outputTokens, err)
	}
	for _, invalid := range []string{
		`{"model":"` + publicModel.ID + `","prompt":""}`,
		`{"model":"` + publicModel.ID + `","prompt":"x","n":0}`,
		`{"model":"` + publicModel.ID + `","prompt":"x","n":11}`,
		`{"model":"` + publicModel.ID + `","prompt":"x","n":1.5}`,
		`{"model":"` + publicModel.ID + `","prompt":"x","output_compression":101}`,
		`{"model":"` + publicModel.ID + `","prompt":"x","partial_images":0}`,
	} {
		if status, _ = generate(secret, invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid status=%d payload=%s", status, invalid)
		}
	}
	if status, _ = generate(secret, `{"model":"`+publicModel.ID+`","prompt":"x","stream":true}`); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("unsupported stream status=%d upstream calls=%d", status, calls)
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = generate(secret, `{"model":"`+publicModel.ID+`","prompt":"x"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("lowest-cost status=%d upstream calls=%d", status, calls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = generate(secret, `{"model":"`+publicModel.ID+`","prompt":"x"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("free-only status=%d upstream calls=%d", status, calls)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "output_tokens", Algorithm: "ceiling", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	status, _ = generate(secret, `{"model":"`+publicModel.ID+`","prompt":"x"}`)
	if status != http.StatusTooManyRequests || calls != 1 {
		t.Fatalf("bounded-policy status=%d upstream calls=%d", status, calls)
	}
	status, _ = generate(denied, `{"model":"`+publicModel.ID+`","prompt":"x"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("denied status=%d upstream calls=%d", status, calls)
	}
}

func TestImageGenerationDoesNotFallbackAfterDispatch(t *testing.T) {
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Length", "1000")
		io.WriteString(w, `{"created":1,"data":[`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"eA=="}]}`)
	}))
	defer fallback.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	primaryConnection, publicModel := publishModel(t, ctx, providerService, owner, "openai", primary.URL+"/v1", "primary-image", []string{"images"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback-image", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback-image", []string{"images"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Images", Scopes: []string{"images:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/images/generations", strings.NewReader(`{"model":"`+publicModel.ID+`","prompt":"x"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || primaryCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("status=%d primary=%d fallback=%d", response.StatusCode, primaryCalls, fallbackCalls)
	}
}

func TestNativeOpenAISpeechUsesScopedModel(t *testing.T) {
	var path, authorization, model string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3speech"))
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "speech-upstream", []string{"audio_speech"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Speech", Scopes: []string{"audio:speech"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No speech", Scopes: []string{"chat:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	speak := func(key, payload string) (int, string, []byte) {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/speech", strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, response.Header.Get("Content-Type"), body
	}
	status, contentType, body := speak(secret, `{"model":"`+publicModel.ID+`","input":"Hello","voice":"alloy","instructions":"Warm","response_format":"mp3","speed":1}`)
	if status != http.StatusOK || contentType != "audio/mpeg" || string(body) != "ID3speech" || path != "/v1/audio/speech" || authorization != "Bearer provider-secret" || model != "speech-upstream" {
		t.Fatalf("status=%d content-type=%s body=%q upstream=%s %s %s", status, contentType, body, path, authorization, model)
	}
	var accounting string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status FROM attempts WHERE state='succeeded'").Scan(&accounting); err != nil || accounting != "unknown" {
		t.Fatalf("accounting=%q err=%v", accounting, err)
	}
	for _, invalid := range []string{
		`{"model":"` + publicModel.ID + `","input":"","voice":"alloy"}`,
		`{"model":"` + publicModel.ID + `","input":"` + strings.Repeat("a", 4097) + `","voice":"alloy"}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":""}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":{}}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":{"id":""}}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","response_format":"mp4"}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","stream_format":"events"}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","stream":1}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","speed":0.1}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","speed":4.1}`,
		`{"model":"` + publicModel.ID + `","input":"x","voice":"alloy","instructions":1}`,
	} {
		if status, _, _ = speak(secret, invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid status=%d payload=%s", status, invalid)
		}
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = speak(secret, `{"model":"`+publicModel.ID+`","input":"x","voice":"alloy"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("lowest-cost status=%d upstream calls=%d", status, calls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = speak(secret, `{"model":"`+publicModel.ID+`","input":"x","voice":"alloy"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("free-only status=%d upstream calls=%d", status, calls)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "output_tokens", Algorithm: "ceiling", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	status, _, _ = speak(secret, `{"model":"`+publicModel.ID+`","input":"x","voice":"alloy"}`)
	if status != http.StatusTooManyRequests || calls != 1 {
		t.Fatalf("strict-policy status=%d upstream calls=%d", status, calls)
	}
	status, _, _ = speak(denied, `{"model":"`+publicModel.ID+`","input":"x","voice":"alloy"}`)
	if status != http.StatusNotFound || calls != 1 {
		t.Fatalf("denied status=%d upstream calls=%d", status, calls)
	}
}

func TestSpeechStreamingFlushesProviderEvents(t *testing.T) {
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["voice"] != "laidback woman" || body["response_format"] != "raw" || body["stream"] != true {
			t.Errorf("upstream body=%v", body)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: {\"type\":\"conversation.item.audio_output.delta\",\"delta\":\"Zmlyc3Q=\"}\n\n")
		response.(http.Flusher).Flush()
		<-finish
		_, _ = io.WriteString(response, "data: [DONE]\n\n")
		response.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "speech-upstream", []string{"audio_speech"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Streaming speech", Scopes: []string{"audio:speech"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/speech", strings.NewReader(`{"model":"`+publicModel.ID+`","input":"Hello","voice":"laidback woman","response_format":"raw","stream":true}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	type result struct {
		response *http.Response
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		resultChannel <- result{response: response, err: requestErr}
	}()
	var httpResult result
	select {
	case httpResult = <-resultChannel:
	case <-time.After(2 * time.Second):
		close(finish)
		t.Fatal("gateway buffered the provider speech stream")
	}
	if httpResult.err != nil {
		close(finish)
		t.Fatal(httpResult.err)
	}
	defer httpResult.response.Body.Close()
	reader := bufio.NewReader(httpResult.response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || httpResult.response.StatusCode != http.StatusOK || httpResult.response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(first, "audio_output.delta") {
		close(finish)
		t.Fatalf("status=%d content-type=%q first=%q err=%v", httpResult.response.StatusCode, httpResult.response.Header.Get("Content-Type"), first, err)
	}
	close(finish)
	remaining, err := io.ReadAll(reader)
	if err != nil || !bytes.Contains(remaining, []byte("[DONE]")) {
		t.Fatalf("remaining=%q err=%v", remaining, err)
	}
}

func TestSpeechDoesNotFallbackAfterDispatch(t *testing.T) {
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("partial"))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3fallback"))
	}))
	defer fallback.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	primaryConnection, publicModel := publishModel(t, ctx, providerService, owner, "openai", primary.URL+"/v1", "primary-speech", []string{"audio_speech"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback-speech", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback-speech", []string{"audio_speech"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Speech", Scopes: []string{"audio:speech"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/speech", strings.NewReader(`{"model":"`+publicModel.ID+`","input":"x","voice":"alloy"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || primaryCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("status=%d primary=%d fallback=%d", response.StatusCode, primaryCalls, fallbackCalls)
	}
}

func TestOpenAIModelListUsesEligibleCountTarget(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	createTarget := func(adapter, name string) (providers.Connection, providers.UpstreamModel) {
		connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: name, Adapter: adapter, BaseURL: "http://127.0.0.1:1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
		if err != nil {
			t.Fatal(err)
		}
		if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
			t.Fatal(err)
		}
		upstream, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, name, []string{"count_tokens"})
		if err != nil {
			t.Fatal(err)
		}
		return connection, upstream
	}
	anthropicConnection, anthropicModel := createTarget("anthropic", "anthropic-count")
	openAIConnection, openAIModel := createTarget("openai", "openai-count")
	publicModel, err := providerService.CreatePublicModel(ctx, owner, "counter", "Counter", "", anthropicModel.ID, []string{"count_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	configure := func(openAIEnabled bool) {
		publicModel, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: anthropicModel.ID, Priority: 1, Enabled: true}, {UpstreamModelID: openAIModel.ID, Priority: 2, Enabled: openAIEnabled}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	configure(true)
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Models", Scopes: []string{"models:read"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{anthropicConnection.ID, openAIConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	list := func() []byte {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/openai/v1/models", nil)
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return body
	}
	if body := list(); !bytes.Contains(body, []byte(`"id":"counter"`)) {
		t.Fatalf("eligible secondary missing: %s", body)
	}
	configure(false)
	if body := list(); bytes.Contains(body, []byte(`"id":"counter"`)) {
		t.Fatalf("disabled OpenAI target still visible: %s", body)
	}
}

func TestNativeModelListsKeepTheirOwnShapes(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	var connections []string
	for _, adapter := range []string{"openai", "anthropic", "gemini"} {
		connection, _ := publishModel(t, ctx, providerService, owner, adapter, "http://127.0.0.1:1", adapter+"-upstream", []string{"chat"})
		connections = append(connections, connection.ID)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Lists", Scopes: []string{"models:read"}, ModelPatterns: []string{"*"}, ConnectionIDs: connections})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	tests := []struct{ path, header, prefix, want string }{{"/api/openai/v1/models", "Authorization", "Bearer ", `"object":"list"`}, {"/api/anthropic/v1/models", "x-api-key", "", `"type":"model"`}, {"/api/gemini/v1beta/models", "x-goog-api-key", "", `"models/`}}
	for _, test := range tests {
		request, _ := http.NewRequest(http.MethodGet, server.URL+test.path, nil)
		request.Header.Set(test.header, test.prefix+secret)
		if test.header == "x-api-key" {
			request.Header.Set("anthropic-version", "2023-06-01")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !strings.Contains(string(body), test.want) {
			t.Errorf("%s = %d %s", test.path, response.StatusCode, body)
		}
	}
}

func TestAnthropicAndGeminiUseNativePathsAndHeaders(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.URL.RequestURI()+"|"+r.Header.Get("x-api-key")+r.Header.Get("x-goog-api-key")+"|"+string(body))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "gemini") {
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Gemini"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Claude"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	anthropic, _ := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	gemini, _ := publishModel(t, ctx, providerService, owner, "gemini", upstream.URL+"/v1beta", "gemini-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Native", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"*-model"}, ConnectionIDs: []string{anthropic.ID, gemini.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	anthropicRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"anthropic-model","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
	anthropicRequest.Header.Set("x-api-key", secret)
	anthropicRequest.Header.Set("anthropic-version", "2023-06-01")
	anthropicResponse, err := http.DefaultClient.Do(anthropicRequest)
	if err != nil {
		t.Fatal(err)
	}
	anthropicBody, _ := io.ReadAll(anthropicResponse.Body)
	anthropicResponse.Body.Close()
	if anthropicResponse.StatusCode != 200 || !strings.Contains(string(anthropicBody), "Claude") {
		t.Fatalf("anthropic = %d %s", anthropicResponse.StatusCode, anthropicBody)
	}
	geminiRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/gemini/v1beta/models/gemini-model:generateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"Hi"}]}]}`))
	geminiRequest.Header.Set("x-goog-api-key", secret)
	geminiResponse, err := http.DefaultClient.Do(geminiRequest)
	if err != nil {
		t.Fatal(err)
	}
	geminiBody, _ := io.ReadAll(geminiResponse.Body)
	geminiResponse.Body.Close()
	if geminiResponse.StatusCode != 200 || !strings.Contains(string(geminiBody), "Gemini") {
		t.Fatalf("gemini = %d %s", geminiResponse.StatusCode, geminiBody)
	}
	if len(seen) != 2 || !strings.Contains(seen[0], "/v1/messages|provider-secret|") || !strings.Contains(seen[0], `"model":"claude-upstream"`) || !strings.Contains(seen[1], "/v1beta/models/gemini-upstream:generateContent|provider-secret|") {
		t.Fatalf("upstream requests = %#v", seen)
	}
}

func TestSSEParserHandlesCRLFMultilineAndAnthropicUsage(t *testing.T) {
	raw := []byte("event: message_start\r\ndata: {\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\r\n\r\nevent: message_delta\r\ndata: {\"usage\":\r\ndata: {\"output_tokens\":3}}\r\n\r\n")
	input, output, _ := parseUsage("anthropic", raw)
	if input == nil || output == nil || *input != 7 || *output != 3 {
		t.Fatalf("usage = %v/%v", input, output)
	}
}

func TestEmbeddingAdmissionUsesRawBodyAndBatchCardinality(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{}`) }))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "embed-upstream", []string{"embeddings"})
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "body_bytes", Algorithm: "ceiling", LimitUnits: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "batch_items", Algorithm: "ceiling", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Embed", Scopes: []string{"embeddings:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, body := range []string{`{                                                                                                    "model":"assistant","input":"a"}`, `{"model":"assistant","input":["a","b"]}`} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/embeddings", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d", response.StatusCode)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls = %d", calls)
	}
}

func TestEmbeddingTokenArrayCountsAsOneInput(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"embed-upstream","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "embed-upstream", []string{"embeddings"})
	if _, err := usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "batch_items", Algorithm: "ceiling", LimitUnits: 1}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Embed token arrays", Scopes: []string{"embeddings:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	accepted := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/embeddings", secret, `{"model":"assistant","input":[1,2]}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("token array status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	rejected := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/embeddings", secret, `{"model":"assistant","input":[[1],[2]]}`)
	if rejected.Code != http.StatusTooManyRequests {
		t.Fatalf("token collection status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestOpenAIEmbeddingUsageDerivesZeroOutputTokens(t *testing.T) {
	input, output, _ := parseUsage("openai", []byte(`{"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	if input == nil || output == nil || *input != 7 || *output != 0 {
		t.Fatalf("usage = %v/%v", input, output)
	}
}

func TestOpenAITopLevelInputTokensAreNotGeneralUsage(t *testing.T) {
	input, output, _ := parseUsage("openai", []byte(`{"input_tokens":7}`))
	if input != nil || output != nil {
		t.Fatalf("usage = %v/%v", input, output)
	}
}

func TestNativeResponsesStreamUsageIsParsed(t *testing.T) {
	input, output, _ := parseUsage("openai", []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"))
	if input == nil || output == nil || *input != 7 || *output != 3 {
		t.Fatalf("usage = %v/%v", input, output)
	}
}

func TestNativeStreamFlushesBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"first\"}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, _ := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "stream-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stream", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"assistant","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() { value, _ := bufio.NewReader(response.Body).ReadString('\n'); line <- value }()
	select {
	case value := <-line:
		if !strings.Contains(value, "first") {
			t.Fatalf("first event = %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("first stream event was buffered")
	}
	close(release)
	response.Body.Close()
}

func TestCrossProtocolHandlerMatrixUsesNativeClientShapes(t *testing.T) {
	tests := []struct {
		client, target, response, wantPath, wantBody string
	}{
		{"openai", "anthropic", `{"id":"msg_1","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`, "/v1/messages", `"object":"chat.completion"`},
		{"openai", "gemini", `{"responseId":"gem_1","candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`, "/v1/models/gemini-upstream:generateContent", `"object":"chat.completion"`},
		{"anthropic", "openai", `{"id":"chat_1","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, "/v1/chat/completions", `"type":"message"`},
		{"anthropic", "gemini", `{"responseId":"gem_1","candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`, "/v1/models/gemini-upstream:generateContent", `"type":"message"`},
		{"gemini", "openai", `{"id":"chat_1","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, "/v1/chat/completions", `"candidates"`},
		{"gemini", "anthropic", `{"id":"msg_1","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`, "/v1/messages", `"candidates"`},
	}
	for _, test := range tests {
		t.Run(test.client+"-to-"+test.target, func(t *testing.T) {
			seenPath, seenModel := "", ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenPath = r.URL.RequestURI()
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				seenModel, _ = body["model"].(string)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.response)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, test.target, upstream.URL+"/v1", test.target+"-upstream", []string{"chat"})
			if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset=? WHERE id=?", test.target, connection.ID); err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Matrix", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request := crossProtocolRequest(t, server.URL, test.client, model.ID, secret)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), test.wantBody) || seenPath != test.wantPath {
				t.Fatalf("status=%d path=%q body=%s", response.StatusCode, seenPath, body)
			}
			if test.target != "gemini" && seenModel != test.target+"-upstream" {
				t.Fatalf("upstream model=%q", seenModel)
			}
			var sourceDialect, sourceOperation, targetDialect, targetOperation string
			var translated bool
			err = store.SystemDB().QueryRowContext(ctx, `SELECT requests.dialect,requests.operation,attempts.target_dialect,attempts.target_operation,attempts.translation_applied FROM requests JOIN attempts ON attempts.request_id=requests.id`).Scan(&sourceDialect, &sourceOperation, &targetDialect, &targetOperation, &translated)
			if err != nil || sourceDialect != test.client || targetDialect != test.target || !translated || sourceOperation == targetOperation {
				t.Fatalf("history=%q/%q -> %q/%q translated=%v err=%v", sourceDialect, sourceOperation, targetDialect, targetOperation, translated, err)
			}
		})
	}
}

func TestResponsesEndpointSupportsEveryTargetFamilyAndRejectsState(t *testing.T) {
	responses := map[string]string{
		"openai":    `{"id":"resp_1","object":"response","status":"completed","model":"openai-upstream","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`,
		"anthropic": `{"id":"msg_1","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`,
		"gemini":    `{"responseId":"gem_1","candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}`,
	}
	for target, upstreamResponse := range responses {
		t.Run(target, func(t *testing.T) {
			calls, path := 0, ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path = r.URL.RequestURI()
				_, _ = io.WriteString(w, upstreamResponse)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, target, upstream.URL+"/v1", target+"-upstream", []string{"chat"})
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Responses", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			call := func(body string) (int, string) {
				request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses", strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+secret)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				value, _ := io.ReadAll(response.Body)
				response.Body.Close()
				return response.StatusCode, string(value)
			}
			status, body := call(`{"model":"` + model.ID + `","input":"Hi","store":false,"max_output_tokens":8}`)
			if status != 200 || !strings.Contains(body, `"object":"response"`) || calls != 1 {
				t.Fatalf("status=%d path=%s body=%s calls=%d", status, path, body, calls)
			}
			status, body = call(`{"model":"` + model.ID + `","input":"Hi","max_output_tokens":8}`)
			var stored map[string]any
			storedID, hasStoredID := "", false
			if json.Unmarshal([]byte(body), &stored) == nil {
				storedID, hasStoredID = stored["id"].(string)
			}
			if status != http.StatusOK || !hasStoredID || stored["store"] != true || calls != 2 {
				t.Fatalf("stored status=%d body=%s calls=%d", status, body, calls)
			}
			request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/openai/v1/responses/"+storedID, nil)
			request.Header.Set("Authorization", "Bearer "+secret)
			retrieved, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			retrievedBody, _ := io.ReadAll(retrieved.Body)
			retrieved.Body.Close()
			if retrieved.StatusCode != http.StatusOK || !strings.Contains(string(retrievedBody), `"store":true`) {
				t.Fatalf("retrieved status=%d body=%s", retrieved.StatusCode, retrievedBody)
			}
			status, body = call(`{"model":"` + model.ID + `","input":"Hi","store":false,"previous_response_id":"resp_old"}`)
			if status != 400 || !strings.Contains(body, "previous_response_id") || calls != 2 {
				t.Fatalf("unsupported status=%d body=%s calls=%d", status, body, calls)
			}
		})
	}
}

func TestStoredResponseCanBeRetrievedAndDeletedOnlyByItsCreatingKey(t *testing.T) {
	var upstreamStore any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamStore = body["store"]
		_, _ = io.WriteString(w, `{"id":"resp_upstream","object":"response","created_at":9007199254740993,"status":"completed","model":"private-upstream-model","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Owner", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, siblingSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Sibling", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_other','other@example.test','Other','hash','member','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, "usr_other", keys.Input{Label: "Other", Scopes: []string{"responses:generate"}, ModelPatterns: []string{"*"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(method, path, token, body string) (int, []byte) {
		request, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		value, _ := io.ReadAll(response.Body)
		return response.StatusCode, value
	}
	status, body := call(http.MethodPost, "/api/openai/v1/responses", secret, `{"model":"`+model.ID+`","input":"Hi","max_output_tokens":8}`)
	var created map[string]any
	if json.Unmarshal(body, &created) != nil || status != http.StatusOK || created["id"] == "resp_upstream" || created["model"] != model.ID || created["store"] != true || upstreamStore != false || !bytes.Contains(body, []byte(`"created_at":9007199254740993`)) {
		t.Fatalf("created status=%d body=%s upstream store=%v", status, body, upstreamStore)
	}
	id, _ := created["id"].(string)
	status, retrieved := call(http.MethodGet, "/api/openai/v1/responses/"+id, secret, "")
	if status != http.StatusOK || !bytes.Equal(body, retrieved) {
		t.Fatalf("retrieved status=%d body=%s", status, retrieved)
	}
	status, _ = call(http.MethodGet, "/api/openai/v1/responses/"+id, otherSecret, "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-owner retrieval status=%d", status)
	}
	status, _ = call(http.MethodGet, "/api/openai/v1/responses/"+id, siblingSecret, "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-key retrieval status=%d", status)
	}
	status, deleted := call(http.MethodDelete, "/api/openai/v1/responses/"+id, secret, "")
	if status != http.StatusOK || !bytes.Contains(deleted, []byte(`"deleted":true`)) {
		t.Fatalf("deleted status=%d body=%s", status, deleted)
	}
	status, _ = call(http.MethodGet, "/api/openai/v1/responses/"+id, secret, "")
	if status != http.StatusNotFound {
		t.Fatalf("retrieval after delete status=%d", status)
	}
	status, _ = call(http.MethodPost, "/api/openai/v1/responses", secret, `{"model":"`+model.ID+`","input":"Hi","stream":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("stored stream status=%d", status)
	}
}

func TestStoredResponseFailureKeepsProviderUsageAndFailsRequest(t *testing.T) {
	upstreamBody := `{"id":"resp_upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	for _, test := range []struct {
		name, body string
		dropTable  bool
	}{{"invalid provider object", `null`, false}, {"database failure", upstreamBody, true}} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.body) }))
			defer upstream.Close()
			fallbackCalls := 0
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalls++
				_, _ = io.WriteString(w, upstreamBody)
			}))
			defer fallback.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
			connections := []string{connection.ID}
			if !test.dropTable {
				secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
				if err != nil {
					t.Fatal(err)
				}
				if err = providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
					t.Fatal(err)
				}
				secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "fallback", []string{"chat"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Weight: 1, Enabled: true}}}); err != nil {
					t.Fatal(err)
				}
				connections = append(connections, secondConnection.ID)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stored", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: connections})
			if err != nil {
				t.Fatal(err)
			}
			if test.dropTable {
				if _, err := store.SystemDB().ExecContext(ctx, `DROP TABLE stored_responses`); err != nil {
					t.Fatal(err)
				}
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses", strings.NewReader(`{"model":"`+model.ID+`","input":"Hi"}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			wantStatus := http.StatusBadGateway
			if test.dropTable {
				wantStatus = http.StatusServiceUnavailable
			}
			if response.StatusCode != wantStatus {
				t.Fatalf("status=%d", response.StatusCode)
			}
			var requestState, attemptState, usageStatus string
			var input, output sql.NullInt64
			if err := store.SystemDB().QueryRowContext(ctx, `SELECT requests.state,attempts.state,attempts.usage_status,attempts.input_tokens,attempts.output_tokens FROM requests JOIN attempts ON attempts.request_id=requests.id`).Scan(&requestState, &attemptState, &usageStatus, &input, &output); err != nil {
				t.Fatal(err)
			}
			wantAttempt := "failed"
			if test.dropTable {
				wantAttempt = "succeeded"
			}
			if requestState != "failed" || attemptState != wantAttempt {
				t.Fatalf("request/attempt=%s/%s", requestState, attemptState)
			}
			if test.dropTable && (usageStatus != "provider_reported" || !input.Valid || input.Int64 != 2 || !output.Valid || output.Int64 != 1) {
				t.Fatalf("usage=%s %v/%v", usageStatus, input, output)
			}
			if !test.dropTable {
				var successes, failures int64
				if err := store.SystemDB().QueryRowContext(ctx, `SELECT success_count,failure_count FROM route_observations WHERE upstream_model_id=? AND operation='responses'`, model.TargetModelID).Scan(&successes, &failures); err != nil || successes != 0 || failures != 1 {
					t.Fatalf("route health=%d/%d error=%v", successes, failures, err)
				}
				if fallbackCalls != 0 {
					t.Fatalf("fallback calls=%d after provider 2xx", fallbackCalls)
				}
			}
		})
	}
}

func TestPresetOperationLimitsDispatch(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "model", []string{"chat"})
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset='together' WHERE id=?", connection.ID); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Limited preset", Scopes: []string{"chat:generate", "responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(path, body string) int {
		request, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := call("/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Hi","store":false}`); status != http.StatusNotFound || calls != 0 {
		t.Fatalf("responses status=%d calls=%d", status, calls)
	}
	if status := call("/api/openai/v1/chat/completions", `{"model":"`+model.ID+`","messages":[{"role":"user","content":"Hi"}]}`); status != http.StatusOK || calls != 1 {
		t.Fatalf("chat status=%d calls=%d", status, calls)
	}
}

func TestGeminiCrossProtocolStreamingRequestsAnUpstreamStream(t *testing.T) {
	var path string
	var stream bool
	var includeUsage bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ = body["stream"].(bool)
		includeUsage, _ = objectMap(body["stream_options"])["include_usage"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stream translation", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/gemini/v1beta/models/"+model.ID+":streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":8}}`))
	request.Header.Set("x-goog-api-key", secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || path != "/v1/chat/completions" || !stream || !includeUsage || !strings.Contains(string(body), `"candidates"`) || !strings.Contains(string(body), "Hello") {
		t.Fatalf("status=%d path=%q stream=%v include_usage=%v body=%s", response.StatusCode, path, stream, includeUsage, body)
	}
}

func TestTranslatedResponseWithoutProviderUsageRemainsUnknown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"msg_1","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn"}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Unknown usage", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var status string
	var input, output sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens FROM attempts WHERE model_id=?", model.ID).Scan(&status, &input, &output); err != nil || status != "unknown" || input.Valid || output.Valid {
		t.Fatalf("usage=%s input=%v output=%v err=%v", status, input, output, err)
	}
}

func TestRequestHistoryRecordsSafeToolMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"time","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Tool history", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	items, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
	if err != nil || len(items) != 1 || len(items[0].Attempts) != 1 {
		t.Fatalf("history=%#v err=%v", items, err)
	}
	attempt := items[0].Attempts[0]
	if attempt.RequestToolCount != 1 || attempt.ResponseToolCalls != 2 || attempt.ToolCallStatus != "completed" {
		t.Fatalf("tool metadata=%#v", attempt)
	}
}

func TestNativeResponsesHistoryRecordsToolMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"weather","arguments":"{}","status":"completed"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Responses tool history", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses", strings.NewReader(`{"model":"`+model.ID+`","input":"Hi","store":false,"tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	items, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
	if err != nil || len(items) != 1 || len(items[0].Attempts) != 1 {
		t.Fatalf("history=%#v err=%v", items, err)
	}
	attempt := items[0].Attempts[0]
	if attempt.RequestToolCount != 1 || attempt.ResponseToolCalls != 1 || attempt.ToolCallStatus != "completed" {
		t.Fatalf("tool metadata=%#v", attempt)
	}
}

func TestResponsesStreamToolMetadataDeduplicatesLifecycle(t *testing.T) {
	raw := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\"}]}}\n\n")
	count, status := parseToolMetadata("responses", raw)
	if count != 1 || status != "completed" {
		t.Fatalf("count=%d status=%q", count, status)
	}
}

func TestOrderedFallbackRecordsEachAttempt(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer failed.Close()
	succeeded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"chat_2","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer succeeded.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, firstModel := publishModel(t, ctx, providerService, owner, "openai", failed.URL+"/v1", "first", []string{"chat"})
	secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "second", Adapter: "openai", BaseURL: succeeded.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "second", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := providerService.ConfigureRoute(ctx, owner, firstModel.ID, firstModel.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: firstModel.TargetModelID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = configured
	firstPrice, err := usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: firstConnection.ID, ModelID: firstModel.ID, InputUSDPerMillion: "1", OutputUSDPerMillion: "2", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	secondPrice, err := usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: secondConnection.ID, ModelID: firstModel.ID, InputUSDPerMillion: "3", OutputUSDPerMillion: "4", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "fallback", Scopes: []string{"chat:generate"}, ModelPatterns: []string{firstModel.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+firstModel.ID+`","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte("fallback")) {
		t.Fatalf("status=%d body=%s", response.StatusCode, raw)
	}
	items, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
	if err != nil || len(items) != 1 || len(items[0].Attempts) != 2 {
		t.Fatalf("history=%#v err=%v", items, err)
	}
	if items[0].Attempts[0].State != "failed" || items[0].Attempts[1].State != "succeeded" || !strings.HasPrefix(items[0].Attempts[0].SelectionReason, "ordered_fallback:") {
		t.Fatalf("attempts=%#v", items[0].Attempts)
	}
	rows, err := store.SystemDB().QueryContext(ctx, "SELECT price_version_id,price_quoted_at FROM attempts ORDER BY ordinal")
	if err != nil {
		t.Fatal(err)
	}
	var prices []string
	var quotes []int64
	for rows.Next() {
		var price string
		var quote int64
		if err := rows.Scan(&price, &quote); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		prices, quotes = append(prices, price), append(quotes, quote)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 || quotes[0] <= 0 || quotes[0] != quotes[1] || prices[0] != firstPrice.ID || prices[1] != secondPrice.ID {
		t.Fatalf("fallback price quotes=%v prices=%v", quotes, prices)
	}
}

func TestAttemptWriterCommitsStreamOnlyAfterFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := newAttemptWriter(recorder, true, 0)
	writer.WriteHeader(http.StatusOK)
	if writer.Committed() {
		t.Fatal("stream headers committed before output")
	}
	_, _ = writer.Write([]byte("data: first\n\n"))
	if writer.Committed() {
		t.Fatal("stream output committed before flush")
	}
	writer.Flush()
	if !writer.Committed() || recorder.Body.String() != "data: first\n\n" {
		t.Fatalf("committed=%v body=%q", writer.Committed(), recorder.Body.String())
	}
	buffered := newAttemptWriter(httptest.NewRecorder(), false, maxInferenceBody)
	_, _ = buffered.Write([]byte("data: buffered\n\n"))
	buffered.Flush()
	if buffered.Committed() {
		t.Fatal("buffered stream committed on flush")
	}
}

func TestNativeNonStreamResponseIsBounded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(response, strings.NewReader(strings.Repeat("x", maxInferenceBody+1)), maxInferenceBody+1)
	}))
	defer upstream.Close()
	recorder := httptest.NewRecorder()
	writer := newAttemptWriter(recorder, false, 0)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	status, _, err := new(Handler).dispatch(writer, request, providers.Target{BaseURL: upstream.URL, AllowPrivateNetwork: true, TimeoutMS: 5000}, "v1/chat", nil, false, "openai", "assistant", false, 0, func() {})
	writer.Commit()
	if err == nil || status != http.StatusOK || recorder.Code != http.StatusBadGateway || recorder.Body.Len() > 1024 {
		t.Fatalf("status=%d gateway=%d bytes=%d err=%v", status, recorder.Code, recorder.Body.Len(), err)
	}
}

func crossProtocolRequest(t *testing.T, baseURL, dialect, model, secret string) *http.Request {
	t.Helper()
	var path, body string
	switch dialect {
	case "openai":
		path, body = "/api/openai/v1/chat/completions", `{"model":"`+model+`","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`
	case "anthropic":
		path, body = "/api/anthropic/v1/messages", `{"model":"`+model+`","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`
	case "gemini":
		path, body = "/api/gemini/v1beta/models/"+model+":generateContent", `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":8}}`
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	switch dialect {
	case "openai":
		request.Header.Set("Authorization", "Bearer "+secret)
	case "anthropic":
		request.Header.Set("x-api-key", secret)
		request.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		request.Header.Set("x-goog-api-key", secret)
	}
	return request
}

func gatewayFixture(t *testing.T) (context.Context, *storage.Store, auth.User, *keys.Service, *providers.Service, *usage.Service) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	_, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, owner, keys.New(store.SystemDB()), providers.New(store.SystemDB(), make([]byte, 32)), usage.New(store.SystemDB())
}
func publishModel(t *testing.T, ctx context.Context, service *providers.Service, owner auth.User, adapter, baseURL, upstreamID string, capabilities []string) (providers.Connection, providers.PublicModel) {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: adapter, Adapter: adapter, BaseURL: baseURL, Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.CreatePublicModel(ctx, owner, adapter+"-model", adapter+" model", "", upstream.ID, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if adapter == "openai" {
		model, err = service.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstream.ID, capabilities)
		if err != nil {
			t.Fatal(err)
		}
	}
	return connection, model
}
