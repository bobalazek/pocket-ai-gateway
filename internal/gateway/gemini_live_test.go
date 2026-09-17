package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"golang.org/x/net/websocket"
)

func TestGeminiLiveWebSocketRoutesSetupAndProxiesAudio(t *testing.T) {
	var key, path, setup string
	upstream := httptest.NewServer(websocket.Server{
		Handshake: func(_ *websocket.Config, request *http.Request) error {
			key, path = request.URL.Query().Get("key"), request.URL.Path
			return nil
		},
		Handler: func(connection *websocket.Conn) {
			if err := websocket.Message.Receive(connection, &setup); err != nil {
				t.Error(err)
				return
			}
			if !strings.Contains(setup, `"model":"models/gemini-live-upstream"`) || !strings.Contains(setup, `"responseModalities":["AUDIO"]`) {
				t.Errorf("setup=%s", setup)
			}
			_ = websocket.Message.Send(connection, `{"setupComplete":{}}`)
			var input string
			if err := websocket.Message.Receive(connection, &input); err != nil || input != `{"realtimeInput":{"audio":{"data":"AQI=","mimeType":"audio/pcm;rate=16000"}}}` {
				t.Errorf("input=%s err=%v", input, err)
				return
			}
			_ = websocket.Message.Send(connection, `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AwQ="}}]}},"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`)
			var ignored string
			_ = websocket.Message.Receive(connection, &ignored)
		},
	})
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "gemini", upstream.URL+"/v1beta", "gemini-live-upstream", []string{"realtime"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Gemini Live", Scopes: []string{"realtime:connect"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/api/gemini/v1beta/live")
	endpoint.Scheme = "ws"
	config, err := websocket.NewConfig(endpoint.String(), "http://client.example")
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("x-goog-api-key", secret)
	client, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = websocket.Message.Send(client, `{"setup":{"model":"models/`+publicModel.ID+`","generationConfig":{"responseModalities":["AUDIO"]}}}`); err != nil {
		t.Fatal(err)
	}
	var message string
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, "setupComplete") {
		t.Fatalf("setup complete=%s err=%v", message, err)
	}
	if err = websocket.Message.Send(client, `{"realtimeInput":{"audio":{"data":"AQI=","mimeType":"audio/pcm;rate=16000"}}}`); err != nil {
		t.Fatal(err)
	}
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, `"data":"AwQ="`) {
		t.Fatalf("audio=%s err=%v", message, err)
	}
	_ = client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var state, status, operation, upstreamID string
		var input, output int64
		err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,target_operation,upstream_model_id,input_tokens,output_tokens FROM attempts WHERE model_id=?", publicModel.ID).Scan(&state, &status, &operation, &upstreamID, &input, &output)
		if err == nil && state == "succeeded" {
			if status != "provider_reported" || operation != "BidiGenerateContent" || upstreamID != "gemini-live-upstream" || input != 4 || output != 2 {
				t.Fatalf("attempt=%s/%s operation=%s upstream=%s usage=%d/%d", state, status, operation, upstreamID, input, output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Gemini Live settlement state=%s status=%s err=%v", state, status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if key != "provider-secret" || path != geminiLivePath {
		t.Fatalf("upstream key=%q path=%q", key, path)
	}
}

func TestGeminiLiveSetupValidation(t *testing.T) {
	for name, message := range map[string]realtimeMessage{
		"binary":        {payload: []byte(`{"setup":{"model":"models/assistant"}}`), payloadType: websocket.BinaryFrame},
		"missing setup": {payload: []byte(`{"realtimeInput":{}}`), payloadType: websocket.TextFrame},
		"bare model":    {payload: []byte(`{"setup":{"model":"assistant"}}`), payloadType: websocket.TextFrame},
		"extra message": {payload: []byte(`{"setup":{"model":"models/assistant"},"realtimeInput":{}}`), payloadType: websocket.TextFrame},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := geminiLiveSetupModel(message); err == nil {
				t.Fatal("invalid Gemini Live setup was accepted")
			}
		})
	}
}
