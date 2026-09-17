package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func TestCustomJavaScriptAdapterTransformsProviderRoundTrip(t *testing.T) {
	var upstreamPath, prefer string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamPath, prefer = request.URL.RequestURI(), request.Header.Get("Prefer")
		_ = json.NewDecoder(request.Body).Decode(&upstreamBody)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"id":"prediction_1","status":"succeeded","output":"A fox"}`)
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "Custom", Adapter: "openai_compatible", BaseURL: upstream.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	connection, err = providerService.GetConnection(ctx, owner, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	requestScript := `(input) => ({path: "predictions?wait=1", headers: {Prefer: "wait=1"}, body: {version: input.model, input: {prompt: input.body.messages[0].content}}})`
	responseScript := `(input) => ({status: 200, body: {id: input.body.id, object: "chat.completion", model: input.model, choices: [{index: 0, message: {role: "assistant", content: input.body.output}, finish_reason: "stop"}], usage: {prompt_tokens: 2, completion_tokens: 1, total_tokens: 3}}})`
	if _, err = providerService.PutAdapterScript(ctx, owner, connection.ID, connection.Revision, providers.AdapterScriptInput{RequestScript: requestScript, ResponseScript: responseScript}); err != nil {
		t.Fatal(err)
	}
	upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-model", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModel.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Custom", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"Draw a fox"}]}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(result.Body)
	result.Body.Close()
	predictionInput, _ := upstreamBody["input"].(map[string]any)
	if result.StatusCode != http.StatusOK || upstreamPath != "/v1/predictions?wait=1" || prefer != "wait=1" || upstreamBody["version"] != "provider-model" || predictionInput["prompt"] != "Draw a fox" || !strings.Contains(string(body), `"content":"A fox"`) {
		t.Fatalf("status=%d path=%q prefer=%q upstream=%#v body=%s", result.StatusCode, upstreamPath, prefer, upstreamBody, body)
	}
}
