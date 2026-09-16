package gateway

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func TestAnthropicStreamRewritesOnlyMessageStartModel(t *testing.T) {
	source := "event: message_start\r\ndata: {\"type\":\"message_start\",\r\ndata: \"message\":{\"model\":\"claude-upstream\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0},\"future_integer\":9007199254740993},\"future\":true}\r\n\r\n" +
		": keepalive\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var output bytes.Buffer
	if err := copyAnthropicStream(&output, strings.NewReader(source), "assistant"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"model":"assistant"`) || strings.Contains(output.String(), "claude-upstream") || !strings.Contains(output.String(), `"future":true`) || !strings.Contains(output.String(), `"future_integer":9007199254740993`) {
		t.Fatalf("rewritten stream = %q", output.String())
	}
	if !strings.HasSuffix(output.String(), ": keepalive\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("unrelated frames changed: %q", output.String())
	}
}

func TestAnthropicStreamRejectsOversizedSingleLine(t *testing.T) {
	source := "data: " + strings.Repeat("x", maxInferenceBody) + "\n\n"
	var output bytes.Buffer
	if err := copyAnthropicStream(&output, strings.NewReader(source), "assistant"); err == nil || output.Len() != 0 {
		t.Fatalf("error = %v, output bytes = %d", err, output.Len())
	}
}

func TestAnthropicStreamRejectsNullMessage(t *testing.T) {
	var output bytes.Buffer
	err := copyAnthropicStream(&output, strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":null}\n\n"), "assistant")
	if !errors.Is(err, errAnthropicStreamInvalid) || output.Len() != 0 {
		t.Fatalf("error = %v, output bytes = %d", err, output.Len())
	}
}

func TestAnthropicStreamDoesNotCompleteTruncatedEvent(t *testing.T) {
	var output bytes.Buffer
	err := copyAnthropicStream(&output, strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}"), "assistant")
	if !errors.Is(err, errAnthropicStreamInvalid) || output.Len() != 0 {
		t.Fatalf("error = %v, output bytes = %d", err, output.Len())
	}
}

func TestInvalidAnthropicMessageStartDoesNotFallback(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		firstCalls.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {invalid}\n\n")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secondCalls.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"second\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer second.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, model := publishModel(t, ctx, providerService, owner, "anthropic", first.URL+"/v1", "first", []string{"chat"})
	secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "anthropic", BaseURL: second.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "second", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	model, err = providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stream", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+model.ID+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("status=%d first=%d second=%d body=%s", response.Code, firstCalls.Load(), secondCalls.Load(), response.Body.String())
	}
}

func TestNativeAnthropicStreamUsesPublicModelBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-upstream\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n")
		response.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(response, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "claude-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Stream", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/anthropic/v1/messages", strings.NewReader(`{"model":"`+model.ID+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
	request.Header.Set("x-api-key", secret)
	request.Header.Set("anthropic-version", "2023-06-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	first := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(response.Body)
		var frame strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			frame.WriteString(line)
			if line == "\n" || readErr != nil {
				first <- frame.String()
				return
			}
		}
	}()
	select {
	case frame := <-first:
		if !strings.Contains(frame, `"model":"`+model.ID+`"`) || strings.Contains(frame, "claude-upstream") {
			t.Fatalf("first frame = %q", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("message_start was buffered")
	}
	once.Do(func() { close(release) })
}
