package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestDispatchClientReleasesConnections(t *testing.T) {
	for _, protocol := range []string{"http1", "http2"} {
		for _, mode := range []string{"complete", "cancel_stream"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				closed := make(chan struct{}, 1)
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, "data: hello\n\n")
					if mode == "cancel_stream" {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
				}))
				upstream.EnableHTTP2 = protocol == "http2"
				upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateClosed {
						closed <- struct{}{}
					}
				}
				upstream.StartTLS()
				defer upstream.Close()
				client := safeClient(5*time.Second, true)
				defer client.CloseIdleConnections()
				client.Transport.(*http.Transport).TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if (response.ProtoMajor == 2) != upstream.EnableHTTP2 {
					t.Fatalf("unexpected protocol %s", response.Proto)
				}
				if mode == "cancel_stream" {
					if line, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil || line != "data: hello\n" {
						t.Fatalf("stream first event = %q, %v", line, err)
					}
					cancel()
				} else if body, err := io.ReadAll(response.Body); err != nil || string(body) != "data: hello\n\n" {
					t.Fatalf("complete response = %q, %v", body, err)
				}
				_ = response.Body.Close()
				select {
				case <-closed:
				case <-time.After(2 * time.Second):
					t.Fatal("single-dispatch client retained its connection after the response ended")
				}
			})
		}
	}
}

func TestSlowUpstreamDoesNotBlockConfigurationWrites(t *testing.T) {
	arrived, unblock := make(chan struct{}, 1), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- struct{}{}
		<-unblock
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "up-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "slow", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer close(unblock)

	go func() {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
		request.Header.Set("Authorization", "Bearer "+secret)
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not dispatched")
	}
	written := make(chan error, 1)
	go func() { written <- providerService.PutCredential(ctx, owner, connection.ID, "rotated", "") }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("credential write waited for the upstream response")
	}
}

func TestStreamTimeoutBoundsIdleGapsNotTotalLength(t *testing.T) {
	for _, test := range []struct {
		name     string
		gap      time.Duration
		complete bool
	}{{"steady", 300 * time.Millisecond, true}, {"stalled", 1500 * time.Millisecond, false}} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for range 5 {
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}],\"usage\":null}\n\n")
					w.(http.Flusher).Flush()
					select {
					case <-time.After(test.gap):
					case <-r.Context().Done():
						return
					}
				}
				_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "up-model", []string{"chat"})
			if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET timeout_ms=1000 WHERE id=?", connection.ID); err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "stream", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model.ID+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if got := strings.Contains(string(body), "[DONE]"); got != test.complete {
				t.Fatalf("complete = %v, body = %s", got, body)
			}
		})
	}
}

func TestDeferredSettlementReleasesStuckAttempt(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", "http://127.0.0.1:9/v1", "up-model", []string{"chat"})
	key, _, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "deferred", Scopes: []string{"chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := usageService.Admit(ctx, usage.AdmissionInput{KeyID: key.ID, ConnectionID: connection.ID, ModelID: model.ID, Operation: "chat/completions", TargetOperation: "chat/completions", Scope: "chat:generate", Dialect: "openai", TargetDialect: "openai", RejectedCandidatesJSON: "[]", EstimatedInputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := usageService.MarkDispatching(ctx, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	handler.deferSettlement(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: "failed", UsageStatus: "unknown", FinalRequest: true})
	handler.retryDeferredSettlements(ctx)
	var state string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state FROM attempts WHERE id=?", admission.AttemptID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("attempt state = %q, %v", state, err)
	}
	if len(handler.pending) != 0 {
		t.Fatalf("pending settlements = %d", len(handler.pending))
	}
}
