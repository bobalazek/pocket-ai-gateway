package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestAnthropicFileReferencesRejectAmbiguousJSONBeforeDispatch(t *testing.T) {
	var calls atomic.Int64
	var forwarded []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		forwarded, _ = io.ReadAll(request.Body)
		if request.URL.Path == "/v1/messages/count_tokens" {
			_, _ = io.WriteString(response, `{"input_tokens":4}`)
			return
		}
		_, _ = io.WriteString(response, `{"id":"msg_guard","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat", "count_tokens"})
	createKey := func(label string) string {
		t.Helper()
		_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: label, Scopes: []string{"files:manage", "chat:generate", "tokens:count"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	secret, other := createKey("Reader"), createKey("Other file owner")
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{10}, 32)).Register(mux)
	upload := func(key string) string {
		t.Helper()
		result := performAnthropicFileUpload(t, mux, key, "note.txt", "text/plain", []byte("file content"), "")
		var file anthropicFile
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &file) != nil {
			t.Fatalf("upload status=%d body=%s", result.Code, result.Body.String())
		}
		return file.ID
	}
	localID, foreignID := upload(secret), upload(other)
	fileBlock := func(id string) string {
		return fmt.Sprintf(`{"type":"document","source":{"type":"file","file_id":%q}}`, id)
	}
	message := func(content string) string {
		return fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":[%s]}]}`, model.ID, content)
	}
	nested := func(block string) string {
		return fmt.Sprintf(`{"type":"tool_result","tool_use_id":"toolu_1","content":[%s]}`, block)
	}
	foreign := fileBlock(foreignID)
	for _, path := range []string{"/api/anthropic/v1/messages", "/api/anthropic/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			for _, test := range []struct {
				name, body string
				status     int
			}{
				{"duplicate messages", fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":[%s]}],"messages":[{"role":"user","content":"safe"}]}`, model.ID, foreign), http.StatusBadRequest},
				{"duplicate source", message(fmt.Sprintf(`{"type":"document","source":{"type":"file","file_id":%q},"source":{"type":"text","data":"safe"}}`, foreignID)), http.StatusBadRequest},
				{"nested duplicate source", message(nested(fmt.Sprintf(`{"type":"document","source":{"type":"file","file_id":%q},"source":{"type":"text","data":"safe"}}`, foreignID))), http.StatusBadRequest},
				{"foreign file", message(nested(foreign)), http.StatusNotFound},
			} {
				t.Run(test.name, func(t *testing.T) {
					before := calls.Load()
					result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(test.body), "application/json")
					if result.Code != test.status || calls.Load() != before {
						t.Fatalf("status=%d upstream=%d body=%s", result.Code, calls.Load()-before, result.Body.String())
					}
				})
			}
			valid := message(nested(fileBlock(localID)))
			before := calls.Load()
			result := performAnthropicFileRequest(t, mux, http.MethodPost, path, secret, strings.NewReader(valid), "application/json")
			if result.Code != http.StatusOK || calls.Load() != before+1 || strings.Contains(string(forwarded), `"file_id"`) || !strings.Contains(string(forwarded), `"data":"file content"`) {
				t.Fatalf("valid nested reference status=%d upstream=%d forwarded=%s body=%s", result.Code, calls.Load()-before, forwarded, result.Body.String())
			}
		})
	}
}

func TestAnthropicMessageBatchParamsRejectAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{
		`{"messages":[],"messages":[]}`,
		`{"messages":[{"content":[{"source":{"type":"file","file_id":"file_one"},"source":{"type":"text","data":"safe"}}]}]}`,
		`{"messages":[],"metadata":` + strings.Repeat("[", 65) + `null` + strings.Repeat("]", 65) + `}`,
	} {
		if err := validateMessageBatchParamsObject(json.RawMessage(raw)); err == nil {
			t.Fatalf("ambiguous or excessive batch params accepted: %s", raw)
		}
	}
	if err := validateMessageBatchParamsObject(json.RawMessage(`{"messages":[{"content":[{"type":"tool_result","content":[{"type":"document","source":{"type":"file","file_id":"file_one"}}]}]}]}`)); err != nil {
		t.Fatalf("valid nested content rejected: %v", err)
	}
}
