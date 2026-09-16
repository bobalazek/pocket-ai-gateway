package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestOpenAICompatibleProviderPresetsUseSharedAdapterContract(t *testing.T) {
	for _, preset := range []string{"openrouter", "zai", "minimax", "ollama", "mistral", "groq", "deepseek", "xai", "together", "fireworks", "cohere", "perplexity"} {
		t.Run(preset, func(t *testing.T) {
			var authorization, path, upstreamModel string
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				authorization = request.Header.Get("Authorization")
				path = request.URL.Path
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				upstreamModel, _ = body["model"].(string)
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, `{"id":"chat_1","object":"chat.completion","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1}}}`)
			}))
			defer upstream.Close()

			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: preset, Preset: preset, Enabled: true, TimeoutMS: 5000})
			if err != nil {
				t.Fatal(err)
			}
			expectedAuthorization := "Bearer provider-secret"
			if preset == "ollama" {
				expectedAuthorization = ""
			} else if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
				t.Fatal(err)
			}
			upstreamModelRecord, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-upstream", []string{"chat"})
			if err != nil {
				t.Fatal(err)
			}
			model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModelRecord.ID, []string{"chat"})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Provider preset", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}

			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"Hi"}]}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			result, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(result.Body)
			result.Body.Close()
			expectedPath := "/v1/chat/completions"
			if preset == "perplexity" {
				expectedPath = "/v1/sonar"
			}
			if result.StatusCode != http.StatusOK || path != expectedPath || authorization != expectedAuthorization || upstreamModel != "provider-upstream" || !strings.Contains(string(body), `"content":"Hello"`) {
				t.Fatalf("status=%d path=%q auth=%q upstream_model=%q body=%s", result.StatusCode, path, authorization, upstreamModel, body)
			}

			records, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
			if err != nil || len(records) != 1 || len(records[0].Attempts) != 1 {
				t.Fatalf("history=%#v err=%v", records, err)
			}
			attempt := records[0].Attempts[0]
			if attempt.UsageStatus != "provider_reported" || attempt.InputTokens == nil || *attempt.InputTokens != 4 || attempt.OutputTokens == nil || *attempt.OutputTokens != 2 || attempt.CacheReadInputTokens == nil || *attempt.CacheReadInputTokens != 1 {
				t.Fatalf("usage=%#v", attempt)
			}
		})
	}
}

func TestNamedOpenAICompatibleResponsesPresetsUseResponsesEndpoint(t *testing.T) {
	for _, preset := range []string{"openrouter", "minimax", "perplexity"} {
		t.Run(preset, func(t *testing.T) {
			var authorization, path, upstreamModel string
			var storeUpstream, hasStore bool
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				authorization, path = request.Header.Get("Authorization"), request.URL.Path
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				upstreamModel, _ = body["model"].(string)
				_, hasStore = body["store"]
				storeUpstream, _ = body["store"].(bool)
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, `{"id":"resp_1","object":"response","status":"completed","model":"provider-model","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
			}))
			defer upstream.Close()

			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: preset, Preset: preset, Enabled: true, TimeoutMS: 5000})
			if err != nil {
				t.Fatal(err)
			}
			if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
				t.Fatal(err)
			}
			upstreamModelRecord, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-upstream", []string{"chat"})
			if err != nil {
				t.Fatal(err)
			}
			model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModelRecord.ID, []string{"chat"})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Responses preset", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}

			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/responses", strings.NewReader(`{"model":"assistant","input":"Hi","store":false}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			result, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(result.Body)
			result.Body.Close()
			if result.StatusCode != http.StatusOK || path != "/v1/responses" || authorization != "Bearer provider-secret" || upstreamModel != "provider-upstream" || !hasStore || storeUpstream || !strings.Contains(string(body), `"object":"response"`) {
				t.Fatalf("status=%d path=%q auth=%q upstream_model=%q store=%v body=%s", result.StatusCode, path, authorization, upstreamModel, storeUpstream, body)
			}
		})
	}
}

func TestNamedPresetOperationContracts(t *testing.T) {
	tests := []struct {
		name, preset, capability, scope, publicPath, upstreamPath, requestBody, responseBody, contentType, want string
	}{
		{"openrouter embeddings", "openrouter", "embeddings", "embeddings:generate", "/api/openai/v1/embeddings", "/v1/embeddings", `{"model":"assistant","input":"hello"}`, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"provider-upstream","usage":{"prompt_tokens":1,"total_tokens":1}}`, "application/json", `"object":"list"`},
		{"perplexity embeddings", "perplexity", "embeddings", "embeddings:generate", "/api/openai/v1/embeddings", "/v1/embeddings", `{"model":"assistant","input":"hello"}`, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"provider-upstream","usage":{"prompt_tokens":1,"total_tokens":1}}`, "application/json", `"object":"list"`},
		{"openrouter speech", "openrouter", "audio_speech", "audio:speech", "/api/openai/v1/audio/speech", "/v1/audio/speech", `{"model":"assistant","input":"Hello","voice":"alloy","response_format":"mp3"}`, "ID3speech", "audio/mpeg", "ID3speech"},
		{"zai images", "zai", "images", "images:generate", "/api/openai/v1/images/generations", "/v1/images/generations", `{"model":"assistant","prompt":"gateway icon"}`, `{"created":1760335349,"data":[{"url":"https://example.com/image.png"}],"content_filter":[{"role":"assistant","level":1}]}`, "application/json", `"data":[{"url":"https://example.com/image.png"}],"content_filter"`},
		{"minimax input tokens", "minimax", "count_tokens", "tokens:count", "/api/openai/v1/responses/input_tokens", "/v1/responses/input_tokens", `{"model":"assistant","input":"hello"}`, `{"object":"response.input_tokens","input_tokens":12}`, "application/json", `"input_tokens":12`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var authorization, path, upstreamModel string
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				authorization, path = request.Header.Get("Authorization"), request.URL.Path
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				upstreamModel, _ = body["model"].(string)
				response.Header().Set("Content-Type", test.contentType)
				_, _ = io.WriteString(response, test.responseBody)
			}))
			defer upstream.Close()

			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: test.preset, Preset: test.preset, Enabled: true, TimeoutMS: 5000})
			if err != nil {
				t.Fatal(err)
			}
			if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
				t.Fatal(err)
			}
			upstreamModelRecord, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-upstream", []string{test.capability})
			if err != nil {
				t.Fatal(err)
			}
			model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModelRecord.ID, []string{test.capability})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Preset operation", Scopes: []string{test.scope}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}

			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+test.publicPath, strings.NewReader(test.requestBody))
			request.Header.Set("Authorization", "Bearer "+secret)
			request.Header.Set("Content-Type", "application/json")
			result, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(result.Body)
			result.Body.Close()
			if result.StatusCode != http.StatusOK || path != test.upstreamPath || authorization != "Bearer provider-secret" || upstreamModel != "provider-upstream" || !strings.Contains(string(body), test.want) {
				t.Fatalf("status=%d path=%q auth=%q upstream_model=%q body=%s", result.StatusCode, path, authorization, upstreamModel, body)
			}
		})
	}
}

func TestMistralPresetTranscriptionContract(t *testing.T) {
	var authorization, path, upstreamModel string
	var upstreamAudio []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorization, path = request.Header.Get("Authorization"), request.URL.Path
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		upstreamModel = request.FormValue("model")
		file, _, err := request.FormFile("file")
		if err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		upstreamAudio, _ = io.ReadAll(file)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"model":"provider-upstream","text":"hello","language":"en","segments":[],"usage":{"prompt_audio_seconds":203,"prompt_tokens":4,"completion_tokens":635,"total_tokens":3264}}`)
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "mistral", Preset: "mistral", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
		t.Fatal(err)
	}
	upstreamModelRecord, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-upstream", []string{"audio_transcription"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModelRecord.ID, []string{"audio_transcription"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Mistral transcription", Scopes: []string{"audio:transcribe"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	body, contentType := audioForm(t, model.ID, "recording.wav", []byte("RIFFaudio"), map[string]string{"response_format": "json"})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/transcriptions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", contentType)
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(result.Body)
	result.Body.Close()
	if result.StatusCode != http.StatusOK || path != "/v1/audio/transcriptions" || authorization != "Bearer provider-secret" || upstreamModel != "provider-upstream" || string(upstreamAudio) != "RIFFaudio" || !strings.Contains(string(responseBody), `"text":"hello"`) {
		t.Fatalf("status=%d path=%q auth=%q upstream_model=%q audio=%q body=%s", result.StatusCode, path, authorization, upstreamModel, upstreamAudio, responseBody)
	}
	records, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
	if err != nil || len(records) != 1 || len(records[0].Attempts) != 1 {
		t.Fatalf("history=%#v err=%v", records, err)
	}
	attempt := records[0].Attempts[0]
	if attempt.UsageStatus != "unknown" || attempt.InputTokens != nil || attempt.OutputTokens != nil || attempt.CostUSD != nil {
		t.Fatalf("usage=%#v", attempt)
	}
}

func TestNativeOpenAICompatibleReasoningFieldsPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		response.Header().Set("Content-Type", "application/json")
		if stream, _ := body["stream"].(bool); stream {
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"r\",\"reasoning_content\":\"rc\",\"reasoning_details\":[{\"type\":\"summary\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(response, `{"id":"chat_1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"answer","reasoning":"r","reasoning_content":"rc","reasoning_details":[{"type":"summary"}]},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "openrouter", Preset: "openrouter", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
		t.Fatal(err)
	}
	upstreamModelRecord, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "provider-upstream", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModelRecord.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Reasoning", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, stream := range []bool{false, true} {
		requestBody := `{"model":"assistant","messages":[{"role":"user","content":"Hi"}],"stream":false}`
		if stream {
			requestBody = `{"model":"assistant","messages":[{"role":"user","content":"Hi"}],"stream":true}`
		}
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+secret)
		result, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(result.Body)
		result.Body.Close()
		for _, want := range []string{`"reasoning":"r"`, `"reasoning_content":"rc"`, `"reasoning_details"`} {
			if result.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(want)) {
				t.Fatalf("stream=%v status=%d body=%s", stream, result.StatusCode, body)
			}
		}
	}
}
