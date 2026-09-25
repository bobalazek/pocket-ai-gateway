package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/operations"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/users"
	"github.com/bobalazek/pocket-ai-gateway/internal/gateway"
	"github.com/bobalazek/pocket-ai-gateway/internal/server"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

type demo struct {
	store    *storage.Store
	upstream *httptest.Server
	handler  http.Handler
	owner    auth.User
	password string
	origin   string
}

// No existing-data argument is accepted: every demo starts with a new store.
func newDemo(ctx context.Context, origin string) (_ *demo, err error) {
	dir, err := os.MkdirTemp("", "pocket-ai-gateway-demo-")
	if err != nil {
		return nil, err
	}
	d := &demo{origin: origin}
	defer func() {
		if err != nil {
			d.close()
			_ = os.RemoveAll(dir)
		}
	}()
	d.store, err = storage.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	d.password, err = credentials.RandomToken(24)
	if err != nil {
		return nil, err
	}
	authService := auth.New(d.store.SystemDB())
	d.owner, _, err = authService.Claim(ctx, auth.ClaimInput{Email: "operator@example.test", DisplayName: "Sample operator", Password: d.password})
	if err != nil {
		return nil, err
	}
	masterKey, err := providers.LoadOrCreateMasterKey(dir, false)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d.upstream = &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(mockProvider)}}
	d.upstream.Start()
	providerService := providers.New(d.store.SystemDB(), masterKey)
	usageService := usage.New(d.store.SystemDB())
	keyService := keys.New(d.store.SystemDB())
	var connectionIDs []string
	for index, name := range []string{"fast", "balanced", "reasoning"} {
		connection, createErr := providerService.CreateConnection(ctx, d.owner, providers.ConnectionInput{
			Name: "Local " + name + " lane", Adapter: "openai_compatible", BaseURL: d.upstream.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000,
		})
		if createErr != nil {
			return nil, createErr
		}
		connectionIDs = append(connectionIDs, connection.ID)
		if err = providerService.PutCredential(ctx, d.owner, connection.ID, "synthetic-demo-credential", ""); err != nil {
			return nil, err
		}
		model, createErr := providerService.CreateUpstreamModel(ctx, d.owner, connection.ID, name+"-chat", []string{"chat"})
		if createErr != nil {
			return nil, createErr
		}
		if _, err = providerService.CreatePublicModel(ctx, d.owner, name+"-chat", strings.ToUpper(name[:1])+name[1:]+" Chat", "Local mock with illustrative pricing and responses.", model.ID, model.Capabilities); err != nil {
			return nil, err
		}
		cacheRate := "0.25"
		_, err = usageService.CreatePrice(ctx, d.owner, usage.PriceInput{
			ConnectionID: connection.ID, ModelID: name + "-chat", InputUSDPerMillion: fmt.Sprint(index + 1), OutputUSDPerMillion: fmt.Sprint((index + 1) * 4), CacheReadUSDPerMillion: &cacheRate,
			Source: "Illustrative local price, not a provider quote", EffectiveFrom: time.Now().AddDate(0, 0, -8).UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, err
		}
	}
	var secrets []string
	for index, person := range []struct{ email, name, key string }{
		{"operator@example.test", "Sample operator", "Product API"},
		{"research@example.test", "Researcher", "Research Assistant"},
		{"support@example.test", "Support analyst", "Support Copilot"},
	} {
		userID := d.owner.ID
		if index > 0 {
			user, code, createErr := users.New(d.store.SystemDB()).Create(ctx, d.owner, users.CreateInput{
				Email: person.email, DisplayName: person.name, Role: "member",
				Grants: users.Grants{Scopes: []string{"chat:generate", "models:read"}, ModelPatterns: []string{"*-chat"}, ConnectionIDs: connectionIDs},
			})
			if createErr != nil {
				return nil, createErr
			}
			if _, _, err = authService.Activate(ctx, auth.ActivateInput{Code: code, Password: d.password, UserAgent: "Synthetic demo"}); err != nil {
				return nil, err
			}
			userID = user.ID
		}
		_, secret, createErr := keyService.Create(ctx, userID, keys.Input{Label: person.key, Scopes: []string{"chat:generate", "models:read"}, ModelPatterns: []string{"*-chat"}, ConnectionIDs: connectionIDs})
		if createErr != nil {
			return nil, createErr
		}
		secrets = append(secrets, secret)
	}
	gatewayHandler := gateway.NewWithMasterKey(d.store.SystemDB(), keyService, providerService, usageService, masterKey, origin)
	operationService := operations.New(d.store, providerService, "dev", func(string) string { return "" })
	d.handler = server.NewRuntime(d.store.SystemDB(), origin, usageService, providerService, operationService, gatewayHandler)
	if err = d.seedRequests(ctx, secrets); err != nil {
		return nil, err
	}
	for {
		count, projectErr := usage.ProjectOutbox(ctx, d.store, 500)
		if projectErr != nil {
			return nil, projectErr
		}
		if count == 0 {
			break
		}
	}
	return d, nil
}

func (d *demo) close() {
	if d.upstream != nil {
		d.upstream.Close()
		d.upstream = nil
	}
	if d.store != nil {
		dir := d.store.DataDir()
		_ = d.store.Close()
		_ = os.RemoveAll(dir)
		d.store = nil
	}
}

func (d *demo) seedRequests(ctx context.Context, secrets []string) error {
	models := []string{"fast-chat", "balanced-chat", "reasoning-chat"}
	tasks := []string{
		"Summarize a fictional support ticket.",
		"Draft a product release note for an imaginary feature.",
		"Classify a sample feedback message.",
		"Suggest a title for a made-up knowledge-base article.",
	}
	for day, count := range []int{8, 13, 11, 20, 17, 24, 31} {
		for index := range count {
			model := models[index%len(models)]
			prompt := fmt.Sprintf("Synthetic demo task %d: %s", index, tasks[(index+day)%len(tasks)])
			status := http.StatusOK
			if day == 6 && index%5 == 4 || day < 6 && index == count-2 {
				status = http.StatusTooManyRequests
				if (index+day)%2 == 0 {
					status = http.StatusServiceUnavailable
				}
				prompt = fmt.Sprintf("MOCK_ERROR_%d: %s", status, prompt)
			}
			var path, body, authHeader, authValue string
			secret := secrets[(index/2+day)%len(secrets)]
			switch (index + day) % 3 {
			case 0:
				path = "/api/openai/v1/chat/completions"
				// Every other OpenAI request streams, so the example shows both response modes.
				body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":4096,"stream":%t}`, model, prompt, index%2 == 0)
				authHeader, authValue = "Authorization", "Bearer "+secret
			case 1:
				path = "/api/anthropic/v1/messages"
				body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":4096}`, model, prompt)
				authHeader, authValue = "x-api-key", secret
			case 2:
				path = "/api/gemini/v1beta/models/" + model + ":generateContent"
				body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":%q}]}],"generationConfig":{"maxOutputTokens":4096}}`, prompt)
				authHeader, authValue = "x-goog-api-key", secret
			}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
			request.Host = strings.TrimPrefix(d.origin, "http://")
			request.Header.Set(authHeader, authValue)
			if authHeader == "x-api-key" {
				request.Header.Set("anthropic-version", "2023-06-01")
			}
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			d.handler.ServeHTTP(response, request)
			if status == http.StatusOK && response.Code != http.StatusOK || status != http.StatusOK && response.Code != http.StatusTooManyRequests && response.Code != http.StatusBadGateway && response.Code != http.StatusServiceUnavailable {
				return fmt.Errorf("seed %s request: unexpected status %d: %s", path, response.Code, response.Body.String())
			}
			requestID := response.Header().Get("X-Pocket-AI-Request-ID")
			if requestID == "" {
				return fmt.Errorf("seed response missing request ID")
			}
			if err := d.backdate(ctx, requestID, int64(6-day)*24*60*60*1000); err != nil {
				return err
			}
		}
	}
	return nil
}

// Shift only this disposable fixture's timestamps before projecting its events.
func (d *demo) backdate(ctx context.Context, requestID string, offset int64) error {
	tx, err := d.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"UPDATE requests SET started_at=started_at-?,finished_at=finished_at-? WHERE id=?", []any{offset, offset, requestID}},
		{"UPDATE attempts SET started_at=started_at-?,finished_at=finished_at-?,first_byte_at=first_byte_at-? WHERE request_id=?", []any{offset, offset, offset, requestID}},
		{"UPDATE event_outbox SET created_at=created_at-?,payload_json=json_set(payload_json,'$.started_at',json_extract(payload_json,'$.started_at')-?) WHERE request_id=?", []any{offset, offset, requestID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func mockProvider(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
		http.NotFound(response, request)
		return
	}
	var input struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
		Stream   bool            `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(response, "Invalid demo request", http.StatusBadRequest)
		return
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		if strings.Contains(string(input.Messages), fmt.Sprintf("MOCK_ERROR_%d", status)) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(status)
			_ = json.NewEncoder(response).Encode(map[string]any{"error": map[string]string{"message": "Synthetic demo provider error", "type": "demo_error", "code": fmt.Sprintf("mock_%d", status)}})
			return
		}
	}
	inputTokens := map[string]int{"fast-chat": 1200, "balanced-chat": 3600, "reasoning-chat": 6400}[input.Model]
	outputTokens := inputTokens / 3
	usage := map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens, "prompt_tokens_details": map[string]int{"cached_tokens": inputTokens / 4}}
	if input.Stream {
		flusher, _ := response.(http.Flusher)
		response.Header().Set("Content-Type", "text/event-stream")
		for index, chunk := range []map[string]any{
			{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": "This is a synthetic"}}}},
			{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": " streamed demo response."}, "finish_reason": "stop"}}},
			{"choices": []any{}, "usage": usage},
		} {
			if index > 0 {
				time.Sleep(40 * time.Millisecond)
			}
			chunk["id"], chunk["object"], chunk["model"] = "chatcmpl_demo", "chat.completion.chunk", input.Model
			encoded, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(response, "data: %s\n\n", encoded)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = io.WriteString(response, "data: [DONE]\n\n")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]any{
		"id": "chatcmpl_demo", "object": "chat.completion", "model": input.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "This is a synthetic demo response from the local mock provider."}, "finish_reason": "stop"}},
		"usage":   usage,
	})
}
