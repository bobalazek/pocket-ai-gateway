package gateway

import (
	"bufio"
	"context"
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

func TestNativeOpenAIForwardingUsesProviderCredentialAndAccounts(t *testing.T) {
	var gotAuthorization, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	New(keyService, providerService, usageService).Register(mux)
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
	var state, status, upstreamID string
	var input, output int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,upstream_model_id,input_tokens,output_tokens FROM attempts WHERE model_id='assistant'").Scan(&state, &status, &upstreamID, &input, &output); err != nil || state != "succeeded" || status != "provider_reported" || upstreamID != "gpt-upstream" || input != 4 || output != 2 {
		t.Fatalf("attempt = %s/%s %d/%d, %v", state, status, input, output, err)
	}
	_ = model
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
	New(keyService, providerService, usageService).Register(mux)
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
	New(keyService, providerService, usageService).Register(mux)
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
	New(keyService, providerService, usageService).Register(mux)
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

func TestOpenAIEmbeddingUsageDerivesZeroOutputTokens(t *testing.T) {
	input, output, _ := parseUsage("openai", []byte(`{"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	if input == nil || output == nil || *input != 7 || *output != 0 {
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
	New(keyService, providerService, usageService).Register(mux)
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
