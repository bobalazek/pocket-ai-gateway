package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	d.owner, _, err = authService.Claim(ctx, auth.ClaimInput{Email: "demo@example.test", DisplayName: "Demo owner", Password: d.password})
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
			Name: "Demo " + name, Adapter: "openai_compatible", BaseURL: d.upstream.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000,
		})
		if createErr != nil {
			return nil, createErr
		}
		connectionIDs = append(connectionIDs, connection.ID)
		if err = providerService.PutCredential(ctx, d.owner, connection.ID, "synthetic-demo-credential", ""); err != nil {
			return nil, err
		}
		model, createErr := providerService.CreateUpstreamModel(ctx, d.owner, connection.ID, "demo-"+name, []string{"chat"})
		if createErr != nil {
			return nil, createErr
		}
		if _, err = providerService.CreatePublicModel(ctx, d.owner, "demo-"+name, "Demo "+name, "Synthetic local mock; prices and responses are illustrative.", model.ID, model.Capabilities); err != nil {
			return nil, err
		}
		cacheRate := "0.25"
		_, err = usageService.CreatePrice(ctx, d.owner, usage.PriceInput{
			ConnectionID: connection.ID, ModelID: "demo-" + name, InputUSDPerMillion: fmt.Sprint(index + 1), OutputUSDPerMillion: fmt.Sprint((index + 1) * 4), CacheReadUSDPerMillion: &cacheRate,
			Source: "Synthetic demo price, not a provider quote", EffectiveFrom: time.Now().AddDate(0, 0, -8).UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, err
		}
	}
	var secrets []string
	for index, label := range []string{"Demo product", "Demo research", "Demo support"} {
		userID := d.owner.ID
		if index > 0 {
			user, code, createErr := users.New(d.store.SystemDB()).Create(ctx, d.owner, users.CreateInput{
				Email: fmt.Sprintf("demo-%d@example.test", index), DisplayName: label, Role: "member",
				Grants: users.Grants{Scopes: []string{"chat:generate", "models:read"}, ModelPatterns: []string{"demo-*"}, ConnectionIDs: connectionIDs},
			})
			if createErr != nil {
				return nil, createErr
			}
			if _, _, err = authService.Activate(ctx, auth.ActivateInput{Code: code, Password: d.password, UserAgent: "Synthetic demo"}); err != nil {
				return nil, err
			}
			userID = user.ID
		}
		_, secret, createErr := keyService.Create(ctx, userID, keys.Input{Label: label, Scopes: []string{"chat:generate", "models:read"}, ModelPatterns: []string{"demo-*"}, ConnectionIDs: connectionIDs})
		if createErr != nil {
			return nil, createErr
		}
		secrets = append(secrets, secret)
	}
	gatewayHandler := gateway.NewWithMasterKey(d.store.SystemDB(), keyService, providerService, usageService, masterKey, origin)
	operationService := operations.New(d.store, providerService, "demo", func(string) string { return "" })
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
	models := []string{"demo-fast", "demo-balanced", "demo-reasoning"}
	for day, count := range []int{8, 13, 11, 20, 17, 24, 31} {
		for index := range count {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Synthetic demo request %d"}],"max_tokens":4096}`, models[index%3], index)
			request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
			request.Host = strings.TrimPrefix(d.origin, "http://")
			request.Header.Set("Authorization", "Bearer "+secrets[(index/2+day)%len(secrets)])
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			d.handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				return fmt.Errorf("seed request: status %d: %s", response.Code, response.Body.String())
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
	for _, query := range []string{
		"UPDATE requests SET started_at=started_at-?,finished_at=finished_at-? WHERE id=?",
		"UPDATE attempts SET started_at=started_at-?,finished_at=finished_at-? WHERE request_id=?",
		"UPDATE event_outbox SET created_at=created_at-?,payload_json=json_set(payload_json,'$.started_at',json_extract(payload_json,'$.started_at')-?) WHERE request_id=?",
	} {
		if _, err := tx.ExecContext(ctx, query, offset, offset, requestID); err != nil {
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
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(response, "Invalid demo request", http.StatusBadRequest)
		return
	}
	inputTokens := map[string]int{"demo-fast": 1200, "demo-balanced": 3600, "demo-reasoning": 6400}[input.Model]
	outputTokens := inputTokens / 3
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]any{
		"id": "chatcmpl_demo", "object": "chat.completion", "model": input.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "This is a synthetic demo response from the local mock provider."}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens, "prompt_tokens_details": map[string]int{"cached_tokens": inputTokens / 4}},
	})
}
