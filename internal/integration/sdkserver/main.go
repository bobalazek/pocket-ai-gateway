package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/gateway"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dataDir, err := os.MkdirTemp("", "pocket-ai-gateway-sdk-")
	must(err)
	defer os.RemoveAll(dataDir)
	store, err := storage.Open(ctx, dataDir)
	must(err)
	defer store.Close()

	upstreamListener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(err)
	upstream := &http.Server{Handler: http.HandlerFunc(upstreamHandler)}
	go upstream.Serve(upstreamListener)
	defer upstream.Shutdown(context.Background())

	owner := auth.User{ID: "usr_sdk", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	_, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "sdk@example.test", "SDK", now, now)
	must(err)
	providerService := providers.New(store.SystemDB(), make([]byte, 32))
	connections := make([]string, 0, 3)
	base := "http://" + upstreamListener.Addr().String()
	for _, adapter := range []string{"openai", "anthropic", "gemini"} {
		connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: adapter, Adapter: adapter, BaseURL: base + "/" + adapter + map[string]string{"openai": "/v1", "anthropic": "/v1", "gemini": "/v1beta"}[adapter], Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
		must(err)
		must(providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""))
		upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, adapter+"-upstream", []string{"chat"})
		must(err)
		_, err = providerService.CreatePublicModel(ctx, owner, "target-"+adapter, "Target "+adapter, "", upstreamModel.ID, []string{"chat"})
		must(err)
		connections = append(connections, connection.ID)
	}
	keyService := keys.New(store.SystemDB())
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Official SDK matrix", Scopes: []string{"chat:generate", "responses:generate", "models:read"}, ModelPatterns: []string{"target-*"}, ConnectionIDs: connections})
	must(err)

	mux := http.NewServeMux()
	gatewayHandler := gateway.New(store.SystemDB(), keyService, providerService, usage.New(store.SystemDB()))
	gatewayHandler.Register(mux)
	go gatewayHandler.RunBackground(ctx)
	gatewayListener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(err)
	server := &http.Server{Handler: mux}
	go server.Serve(gatewayListener)
	encoded, _ := json.Marshal(map[string]string{"url": "http://" + gatewayListener.Addr().String(), "key": secret})
	fmt.Println(string(encoded))
	<-ctx.Done()
	server.Shutdown(context.Background())
}

func upstreamHandler(response http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	if strings.Contains(string(body), "Cancel me") {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
	stream := strings.Contains(string(body), `"stream":true`) || strings.Contains(request.URL.Path, "streamGenerateContent")
	response.Header().Set("Content-Type", map[bool]string{true: "text/event-stream", false: "application/json"}[stream])
	target := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")[0]
	if stream {
		switch target {
		case "openai":
			io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		case "anthropic":
			io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "gemini":
			io.WriteString(response, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n")
		}
		return
	}
	switch target {
	case "openai":
		if strings.HasSuffix(request.URL.Path, "/responses") {
			io.WriteString(response, `{"id":"resp_1","object":"response","status":"completed","model":"openai-upstream","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			return
		}
		io.WriteString(response, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	case "anthropic":
		io.WriteString(response, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	case "gemini":
		io.WriteString(response, `{"responseId":"gemini_1","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
