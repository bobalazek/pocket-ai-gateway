package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/protocol"
)

func TestCompletionOutputReservationAccountsForPromptsAndCandidates(t *testing.T) {
	request := protocol.OpenAICompletionRequest{PromptCount: 2, Candidates: 3}
	if estimate, bounded, err := completionOutputReservation(8, request); err != nil || estimate != 48 || !bounded {
		t.Fatalf("bounded estimate=%d bounded=%v error=%v", estimate, bounded, err)
	}
	if estimate, bounded, err := completionOutputReservation(0, request); err != nil || estimate != 24_576 || bounded {
		t.Fatalf("default estimate=%d bounded=%v error=%v", estimate, bounded, err)
	}
	if _, _, err := completionOutputReservation(9_007_199_254_740_991, protocol.OpenAICompletionRequest{PromptCount: 2048, Candidates: 128}); err == nil {
		t.Fatal("overflowing reservation was accepted")
	}
}

func TestOpenAICompletionBatchInputUsesDirectValidation(t *testing.T) {
	valid := []byte(`{"custom_id":"one","method":"POST","url":"/v1/completions","body":{"model":"legacy","prompt":["one","two"],"max_tokens":8,"n":2,"stream_options":null}}` + "\n")
	items, model, err := parseOpenAIBatchInput(valid, "/v1/completions")
	if err != nil || len(items) != 1 || model != "legacy" {
		t.Fatalf("items=%#v model=%q error=%v", items, model, err)
	}
	invalid := []byte(`{"custom_id":"one","method":"POST","url":"/v1/completions","body":{"model":"legacy","prompt":"x","stream":true}}` + "\n")
	if _, _, err := parseOpenAIBatchInput(invalid, "/v1/completions"); err == nil {
		t.Fatal("streaming Completion Batch was accepted")
	}
}

func TestOpenAICompletionDirectPathValidatesAndNormalizes(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body map[string]json.RawMessage
		if request.URL.Path != "/v1/completions" || json.NewDecoder(request.Body).Decode(&body) != nil || string(body["model"]) != `"legacy-upstream"` || string(body["prompt"]) != `"Complete"` {
			t.Errorf("upstream request path=%s body=%s", request.URL.Path, body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"id":"cmpl_1","object":"text_completion","created":1,"model":"legacy-upstream","choices":[{"text":"done","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "legacy-upstream", []string{"completions"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Completion", Scopes: []string{"completions:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)

	invalid := completionRequestRecorder(mux, secret, `{"model":"assistant","prompt":"Complete","messages":[]}`)
	if invalid.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("invalid status=%d calls=%d body=%s", invalid.Code, calls.Load(), invalid.Body.String())
	}
	valid := completionRequestRecorder(mux, secret, `{"model":"assistant","prompt":"Complete","max_tokens":8,"logprobs":2,"stream_options":null}`)
	if valid.Code != http.StatusOK || calls.Load() != 1 || !bytes.Contains(valid.Body.Bytes(), []byte(`"model":"assistant"`)) {
		t.Fatalf("valid status=%d calls=%d body=%s", valid.Code, calls.Load(), valid.Body.String())
	}
	requests, _, err := usageService.ListRequests(context.Background(), owner, usage.UsageQuery{})
	if err != nil || len(requests) != 1 || len(requests[0].Attempts) != 1 || requests[0].Attempts[0].UsageStatus != "provider_reported" {
		t.Fatalf("requests=%#v error=%v", requests, err)
	}
}

func completionRequestRecorder(handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
