package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestInferenceResponsesExposeGatewayRequestID(t *testing.T) {
	tests := []struct {
		dialect, response string
	}{
		{"openai", `{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`},
		{"anthropic", `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`},
		{"gemini", `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]} ,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`},
	}
	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(pocketAIRequestIDHeader, "provider-value")
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, test.response)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, test.dialect, upstream.URL+map[string]string{"openai": "/v1", "anthropic": "/v1", "gemini": "/v1beta"}[test.dialect], test.dialect+"-upstream", []string{"chat"})
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "request ID", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			response, err := http.DefaultClient.Do(crossProtocolRequest(t, server.URL, test.dialect, model.ID, secret))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			requestID := response.Header.Get(pocketAIRequestIDHeader)
			var storedID string
			if response.StatusCode != http.StatusOK || !strings.HasPrefix(requestID, "req_") || store.SystemDB().QueryRowContext(ctx, "SELECT id FROM requests").Scan(&storedID) != nil || storedID != requestID {
				t.Fatalf("status=%d header=%q stored=%q", response.StatusCode, requestID, storedID)
			}
		})
	}
}

func TestAttemptWriterPreservesGatewayRequestIDOnStreamCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set(pocketAIRequestIDHeader, "req_gateway")
	writer := newAttemptWriter(recorder, true, 0)
	writer.Header().Set(pocketAIRequestIDHeader, "req_provider")
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = writer.Write([]byte("data: {}\n\n"))
	writer.Flush()
	if recorder.Header().Get(pocketAIRequestIDHeader) != "req_gateway" {
		t.Fatalf("headers=%v", recorder.Header())
	}
}
