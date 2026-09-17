package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"golang.org/x/net/websocket"
)

func TestRealtimeWebSocketProxiesFramesAndAccountsUsage(t *testing.T) {
	var authorization, upstreamModel, blockedQuery string
	upstream := httptest.NewServer(websocket.Server{
		Handshake: func(_ *websocket.Config, request *http.Request) error {
			authorization = request.Header.Get("Authorization")
			upstreamModel = request.URL.Query().Get("model")
			blockedQuery = request.URL.Query().Get("client_control")
			return nil
		},
		Handler: func(connection *websocket.Conn) {
			if err := websocket.Message.Send(connection, `{"type":"session.created"}`); err != nil {
				t.Error(err)
				return
			}
			var input string
			if err := websocket.Message.Receive(connection, &input); err != nil {
				t.Error(err)
				return
			}
			if input != `{"type":"response.create"}` {
				t.Errorf("client event=%s", input)
			}
			if err := websocket.Message.Send(connection, `{"type":"response.done","response":{"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`); err != nil {
				t.Error(err)
			}
		},
	})
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "gpt-realtime-upstream", []string{"realtime"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Realtime", Scopes: []string{"realtime:connect"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/api/openai/v1/realtime?model=" + url.QueryEscape(publicModel.ID) + "&client_control=blocked")
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
	var message string
	if err := websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, "session.created") {
		t.Fatalf("session event=%q err=%v", message, err)
	}
	if err := websocket.Message.Send(client, `{"type":"response.create"}`); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Receive(client, &message); err != nil || !strings.Contains(message, "response.done") {
		t.Fatalf("response event=%q err=%v", message, err)
	}
	_ = client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var state, status, upstreamID string
		var input, output int64
		err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,upstream_model_id,input_tokens,output_tokens FROM attempts WHERE model_id=?", publicModel.ID).Scan(&state, &status, &upstreamID, &input, &output)
		if err == nil && state == "succeeded" {
			if status != "provider_reported" || upstreamID != "gpt-realtime-upstream" || input != 4 || output != 2 {
				t.Fatalf("attempt=%s/%s upstream=%s usage=%d/%d", state, status, upstreamID, input, output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("realtime settlement state=%s status=%s err=%v", state, status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authorization != "Bearer provider-secret" || upstreamModel != "gpt-realtime-upstream" {
		t.Fatalf("upstream auth=%q model=%q", authorization, upstreamModel)
	}
	if blockedQuery != "" {
		t.Fatalf("client query parameter was forwarded upstream: %q", blockedQuery)
	}
}

func TestRealtimeRequiresWebSocketUpgrade(t *testing.T) {
	ctx, store, _, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/api/openai/v1/realtime?model=assistant", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "WebSocket upgrade") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	_ = ctx
}

func TestRealtimeHandshakeHonorsTargetTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer connection.Close()
			time.Sleep(time.Second)
		}
	}()
	started := time.Now()
	_, err = dialRealtime(context.Background(), providers.Target{BaseURL: "http://" + listener.Addr().String() + "/v1", UpstreamID: "realtime", TimeoutMS: 50, AllowPrivateNetwork: true})
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("handshake err=%v elapsed=%s", err, time.Since(started))
	}
}
