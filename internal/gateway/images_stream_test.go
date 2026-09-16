package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func TestOpenAIImageGenerationStreamForwardsAndAccounts(t *testing.T) {
	var upstreamBody map[string]json.RawMessage
	stream := imageGenerationPartialEvent(0, "eA==") + imageGenerationCompletedEvent(5, 7)
	ctx, database, handler, secret, primaryCalls, fallbackCalls := imageGenerationStreamFixture(t, "gpt-image-1", func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(response, stream)
	})

	response := performImageGenerationStream(handler, secret, `{"model":"assistant","prompt":"A black dot","stream":true,"partial_images":2}`)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || response.Body.String() != stream {
		t.Fatalf("status=%d content-type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if string(upstreamBody["model"]) != `"gpt-image-1"` || string(upstreamBody["stream"]) != "true" || string(upstreamBody["partial_images"]) != "2" {
		t.Fatalf("upstream request=%v", upstreamBody)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 0 {
		t.Fatalf("primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
	var state, usageStatus string
	var inputTokens, outputTokens int64
	if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens FROM attempts`).Scan(&state, &usageStatus, &inputTokens, &outputTokens); err != nil || state != "succeeded" || usageStatus != "provider_reported" || inputTokens != 5 || outputTokens != 7 {
		t.Fatalf("attempt=%s/%s usage=%d/%d err=%v", state, usageStatus, inputTokens, outputTokens, err)
	}
}

func TestOpenAIImageGenerationStreamRejectsInvalidRequestsBeforeDispatch(t *testing.T) {
	_, _, handler, secret, primaryCalls, fallbackCalls := imageGenerationStreamFixture(t, "gpt-image-1", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, imageGenerationCompletedEvent(1, 1))
	})
	tests := []string{
		`{"model":"assistant","prompt":"x","stream":true,"partial_images":-1}`,
		`{"model":"assistant","prompt":"x","stream":true,"partial_images":4}`,
		`{"model":"assistant","prompt":"x","stream":true,"partial_images":1.5}`,
		`{"model":"assistant","prompt":"x","stream":true,"partial_images":"1"}`,
		`{"model":"assistant","prompt":"x","stream":false,"partial_images":1}`,
		`{"model":"assistant","prompt":"x","partial_images":1}`,
		`{"model":"assistant","prompt":"x","stream":"true"}`,
	}
	for _, body := range tests {
		response := performImageGenerationStream(handler, secret, body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("status=%d body=%s request=%s", response.Code, response.Body.String(), body)
		}
	}
	if primaryCalls.Load() != 0 || fallbackCalls.Load() != 0 {
		t.Fatalf("invalid requests dispatched: primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestOpenAIImageGenerationStreamRequiresGPTImageModel(t *testing.T) {
	for _, upstreamModel := range []string{"dall-e-3", "image-upstream"} {
		t.Run(upstreamModel, func(t *testing.T) {
			_, _, handler, secret, primaryCalls, fallbackCalls := imageGenerationStreamFixture(t, upstreamModel, func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(response, imageGenerationCompletedEvent(1, 1))
			})
			response := performImageGenerationStream(handler, secret, `{"model":"assistant","prompt":"x","stream":true}`)
			if response.Code != http.StatusNotFound || primaryCalls.Load() != 0 || fallbackCalls.Load() != 0 {
				t.Fatalf("status=%d body=%s primary=%d fallback=%d", response.Code, response.Body.String(), primaryCalls.Load(), fallbackCalls.Load())
			}
		})
	}
}

func TestOpenAIImageGenerationStreamRejectsInvalidTerminalWithoutFallback(t *testing.T) {
	tests := map[string]string{
		"missing terminal": imageGenerationPartialEvent(0, "eA=="),
		"wrong terminal":   "event: image_generation.completed\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"eA==\",\"background\":\"auto\",\"created_at\":1,\"output_format\":\"png\",\"partial_image_index\":0,\"quality\":\"high\",\"size\":\"1024x1024\"}\n\n",
		"malformed SSE":    "event: image_generation.completed\ndata: {\"type\":\n\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, database, handler, secret, primaryCalls, fallbackCalls := imageGenerationStreamFixture(t, "gpt-image-1", func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(response, stream)
			})
			_ = performImageGenerationStream(handler, secret, `{"model":"assistant","prompt":"x","stream":true,"partial_images":1}`)
			if primaryCalls.Load() != 1 || fallbackCalls.Load() != 0 {
				t.Fatalf("primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
			}
			var state, usageStatus string
			if err := database.QueryRowContext(ctx, `SELECT state,usage_status FROM attempts`).Scan(&state, &usageStatus); err != nil || state != "failed" || usageStatus != "unknown" {
				t.Fatalf("attempt=%s/%s err=%v", state, usageStatus, err)
			}
		})
	}
}

func TestOpenAIImageGenerationStreamAccountsAfterLargePartial(t *testing.T) {
	largePartial := imageGenerationPartialEvent(0, strings.Repeat("A", 9<<20))
	ctx, database, handler, secret, _, _ := imageGenerationStreamFixture(t, "gpt-image-1", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, largePartial)
		_, _ = io.WriteString(response, imageGenerationCompletedEvent(11, 13))
	})
	response := performImageGenerationStream(handler, secret, `{"model":"assistant","prompt":"x","stream":true,"partial_images":1}`)
	if response.Code != http.StatusOK || response.Body.Len() <= 8<<20 {
		t.Fatalf("status=%d response bytes=%d", response.Code, response.Body.Len())
	}
	var state, usageStatus string
	var inputTokens, outputTokens int64
	if err := database.QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens FROM attempts`).Scan(&state, &usageStatus, &inputTokens, &outputTokens); err != nil || state != "succeeded" || usageStatus != "provider_reported" || inputTokens != 11 || outputTokens != 13 {
		t.Fatalf("attempt=%s/%s usage=%d/%d err=%v", state, usageStatus, inputTokens, outputTokens, err)
	}
}

func imageGenerationStreamFixture(t *testing.T, upstreamModel string, primary http.HandlerFunc) (context.Context, *sql.DB, http.Handler, string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var primaryCalls, fallbackCalls atomic.Int64
	primaryServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		primaryCalls.Add(1)
		primary(response, request)
	}))
	t.Cleanup(primaryServer.Close)
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, imageGenerationCompletedEvent(1, 1))
	}))
	t.Cleanup(fallbackServer.Close)

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	t.Cleanup(func() { _ = store.Close() })
	primaryConnection, primaryModel, model := publishImageGenerationStreamModel(t, ctx, store.SystemDB(), providerService, owner, primaryServer.URL+"/v1", upstreamModel, "assistant")
	fallbackConnection, fallbackModel, _ := publishImageGenerationStreamModel(t, ctx, store.SystemDB(), providerService, owner, fallbackServer.URL+"/v1", upstreamModel, "")
	if _, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: primaryModel.ID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Image stream", Scopes: []string{"images:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	return ctx, store.SystemDB(), mux, secret, &primaryCalls, &fallbackCalls
}

func publishImageGenerationStreamModel(t *testing.T, ctx context.Context, database *sql.DB, service *providers.Service, owner auth.User, baseURL, upstreamID, publicID string) (providers.Connection, providers.UpstreamModel, providers.PublicModel) {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: upstreamID, Preset: "openai", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", baseURL, connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"images"})
	if err != nil {
		t.Fatal(err)
	}
	var model providers.PublicModel
	if publicID != "" {
		model, err = service.CreatePublicModel(ctx, owner, publicID, "Image stream", "", upstream.ID, []string{"images"})
		if err != nil {
			t.Fatal(err)
		}
	}
	return connection, upstream, model
}

func performImageGenerationStream(handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/images/generations", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func imageGenerationPartialEvent(index int, image string) string {
	return "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"" + image + "\",\"background\":\"auto\",\"created_at\":1,\"output_format\":\"png\",\"partial_image_index\":" + strconv.Itoa(index) + ",\"quality\":\"high\",\"size\":\"1024x1024\"}\n\n"
}

func imageGenerationCompletedEvent(inputTokens, outputTokens int) string {
	return "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"eA==\",\"background\":\"auto\",\"created_at\":1,\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\",\"usage\":{\"input_tokens\":" + strconv.Itoa(inputTokens) + ",\"input_tokens_details\":{\"image_tokens\":0,\"text_tokens\":" + strconv.Itoa(inputTokens) + "},\"output_tokens\":" + strconv.Itoa(outputTokens) + ",\"total_tokens\":" + strconv.Itoa(inputTokens+outputTokens) + "}}\n\n"
}
