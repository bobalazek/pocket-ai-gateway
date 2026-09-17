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

func TestLiveWebSocketRoutesFirstEventAndProxiesAudio(t *testing.T) {
	var authorization, path, start string
	upstream := httptest.NewServer(websocket.Server{
		Handshake: func(_ *websocket.Config, request *http.Request) error {
			authorization, path = request.Header.Get("Authorization"), request.URL.Path
			return nil
		},
		Handler: func(connection *websocket.Conn) {
			if err := websocket.Message.Receive(connection, &start); err != nil {
				t.Error(err)
				return
			}
			if !strings.Contains(start, `"type":"session.start"`) || !strings.Contains(start, `"model":"gpt-live-1"`) {
				t.Errorf("start=%s", start)
			}
			_ = websocket.Message.Send(connection, `{"type":"session.started","session":{"id":"live_1","model":"gpt-live-1"}}`)
			var audio string
			if err := websocket.Message.Receive(connection, &audio); err != nil || audio != `{"type":"input_audio.append","audio":"AQI="}` {
				t.Errorf("audio=%s err=%v", audio, err)
				return
			}
			_ = websocket.Message.Send(connection, `{"type":"output_audio.delta","delta":"AwQ=","start_ms":0,"end_ms":10}`)
			_ = websocket.Message.Send(connection, `{"type":"response.event","event":{"type":"response.completed","response":{"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}}`)
			var closeEvent string
			if err := websocket.Message.Receive(connection, &closeEvent); err != nil || closeEvent != `{"type":"session.close"}` {
				t.Errorf("close=%s err=%v", closeEvent, err)
				return
			}
			_ = websocket.Message.Send(connection, `{"type":"session.closed","session":{"id":"live_1","model":"gpt-live-1"},"usage":{"seconds":1}}`)
		},
	})
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "gpt-live-1", []string{"realtime"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Live", Scopes: []string{"realtime:connect"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/api/openai/v1/live")
	endpoint.Scheme = "ws"
	config, err := websocket.NewConfig(endpoint.String(), "http://client.example")
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer "+secret)
	client, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = websocket.Message.Send(client, `{"type":"session.start","event_id":"start_1","session":{"type":"live","model":"`+publicModel.ID+`","audio":{"input":{"format":{"type":"audio/pcm","rate":24000}}}}}`); err != nil {
		t.Fatal(err)
	}
	var message string
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, `"type":"session.started"`) || !strings.Contains(message, `"model":"`+publicModel.ID+`"`) {
		t.Fatalf("started=%s err=%v", message, err)
	}
	if err = websocket.Message.Send(client, `{"type":"input_audio.append","audio":"AQI="}`); err != nil {
		t.Fatal(err)
	}
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, `"type":"output_audio.delta"`) || !strings.Contains(message, `"delta":"AwQ="`) {
		t.Fatalf("output audio=%s err=%v", message, err)
	}
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, `"type":"response.event"`) {
		t.Fatalf("response event=%s err=%v", message, err)
	}
	if err = websocket.Message.Send(client, `{"type":"session.close"}`); err != nil {
		t.Fatal(err)
	}
	if err = websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, `"type":"session.closed"`) || !strings.Contains(message, `"model":"`+publicModel.ID+`"`) {
		t.Fatalf("closed=%s err=%v", message, err)
	}
	_ = client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var state, status, operation, upstreamID string
		var input, output int64
		err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,target_operation,upstream_model_id,input_tokens,output_tokens FROM attempts WHERE model_id=?", publicModel.ID).Scan(&state, &status, &operation, &upstreamID, &input, &output)
		if err == nil && state == "succeeded" {
			if status != "provider_reported" || operation != "live" || upstreamID != "gpt-live-1" || input != 4 || output != 2 {
				t.Fatalf("attempt=%s/%s operation=%s upstream=%s usage=%d/%d", state, status, operation, upstreamID, input, output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Live settlement state=%s status=%s err=%v", state, status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authorization != "Bearer provider-secret" || path != "/v1/live" {
		t.Fatalf("upstream auth=%q path=%q", authorization, path)
	}
}

func TestLiveStartValidation(t *testing.T) {
	for name, message := range map[string]realtimeMessage{
		"binary":        {payload: []byte(`{"type":"session.start","session":{"model":"assistant"}}`), payloadType: websocket.BinaryFrame},
		"wrong event":   {payload: []byte(`{"type":"session.update","session":{"model":"assistant"}}`), payloadType: websocket.TextFrame},
		"missing model": {payload: []byte(`{"type":"session.start","session":{}}`), payloadType: websocket.TextFrame},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := liveStartModel(message); err == nil {
				t.Fatal("invalid Live start was accepted")
			}
		})
	}
}
