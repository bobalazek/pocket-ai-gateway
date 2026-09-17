package gateway

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestOpenAIImageEditPreservesMultipartAndEnforcement(t *testing.T) {
	var upstreamModel, prompt, quality string
	var primaryCalls, fallbackCalls int
	var failPrimary atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		primaryCalls++
		if failPrimary.Load() {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.URL.Path != "/v1/images/edits" {
			t.Errorf("upstream path = %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(maxImageMultipartBody); err != nil {
			t.Errorf("parse upstream multipart: %v", err)
			return
		}
		upstreamModel, prompt, quality = request.FormValue("model"), request.FormValue("prompt"), request.FormValue("quality")
		response.Header().Set("Content-Type", "application/json")
		io.WriteString(response, `{"created":1,"data":[{"b64_json":"eA=="}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fallbackCalls++
		io.WriteString(response, `{"created":1,"data":[]}`)
	}))
	defer fallback.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	primaryConnection, publicModel := publishModel(t, ctx, providerService, owner, "openai", primary.URL+"/v1", "gpt-image-1", []string{"image_edit"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "image edit fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback-image", []string{"image_edit"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Image edits", Scopes: []string{"images:edit"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{primaryConnection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No edits", Scopes: []string{"images:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{primaryConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body, contentType := imageForm(t, map[string]string{"model": publicModel.ID, "prompt": "Add a hat", "quality": "high", "n": "1"}, []imageUpload{{"image", "source.png", makePNG(t, 2, 2)}})
	status, responseType, responseBody := postImage(t, server.URL, "images/edits", secret, body, contentType)
	if status != http.StatusOK || responseType != "application/json" || !bytes.Contains(responseBody, []byte(`"b64_json":"eA=="`)) || upstreamModel != "gpt-image-1" || prompt != "Add a hat" || quality != "high" || primaryCalls != 1 {
		t.Fatalf("status=%d type=%q body=%s model=%q prompt=%q quality=%q calls=%d", status, responseType, responseBody, upstreamModel, prompt, quality, primaryCalls)
	}
	var accounting string
	var inputTokens, outputTokens int64
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens FROM attempts WHERE state='succeeded'").Scan(&accounting, &inputTokens, &outputTokens); err != nil || accounting != "provider_reported" || inputTokens != 3 || outputTokens != 2 {
		t.Fatalf("accounting=%q input=%d output=%d err=%v", accounting, inputTokens, outputTokens, err)
	}
	streamBody, streamType := imageForm(t, map[string]string{"model": publicModel.ID, "prompt": "Add a hat", "stream": "true", "n": "2"}, []imageUpload{{"image", "source.png", makePNG(t, 2, 2)}})
	if status, _, _ = postImage(t, server.URL, "images/edits", secret, streamBody, streamType); status != http.StatusBadRequest || primaryCalls != 1 {
		t.Fatalf("stream status=%d calls=%d", status, primaryCalls)
	}
	if status, _, _ = postImage(t, server.URL, "images/edits", denied, body, contentType); status != http.StatusNotFound || primaryCalls != 1 {
		t.Fatalf("denied status=%d calls=%d", status, primaryCalls)
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/edits", secret, body, contentType); status != http.StatusNotFound || primaryCalls != 1 {
		t.Fatalf("lowest-cost status=%d calls=%d", status, primaryCalls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/edits", secret, body, contentType); status != http.StatusNotFound || primaryCalls != 1 {
		t.Fatalf("free-only status=%d calls=%d", status, primaryCalls)
	}
	fixed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	failPrimary.Store(true)
	if status, _, _ = postImage(t, server.URL, "images/edits", secret, body, contentType); status != http.StatusBadGateway || primaryCalls != 2 || fallbackCalls != 0 {
		t.Fatalf("failure status=%d primary=%d fallback=%d", status, primaryCalls, fallbackCalls)
	}
	var failed int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed attempts=%d err=%v", failed, err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, fixed.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 100}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/edits", secret, body, contentType); status != http.StatusTooManyRequests || primaryCalls != 2 {
		t.Fatalf("token-policy status=%d calls=%d", status, primaryCalls)
	}
}

func TestOpenAIImageEditStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/images/edits" || request.ParseMultipartForm(maxImageMultipartBody) != nil || request.FormValue("model") != "gpt-image-1" || request.FormValue("stream") != "true" || request.FormValue("partial_images") != "1" {
			t.Errorf("invalid upstream image-edit request")
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: image_edit.partial_image\ndata: {\"type\":\"image_edit.partial_image\",\"b64_json\":\"eA==\",\"background\":\"auto\",\"created_at\":1,\"output_format\":\"png\",\"partial_image_index\":0,\"quality\":\"high\",\"size\":\"1024x1024\"}\n\n")
		_, _ = io.WriteString(response, "event: image_edit.completed\ndata: {\"type\":\"image_edit.completed\",\"b64_json\":\"eA==\",\"background\":\"auto\",\"created_at\":1,\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\",\"usage\":{\"input_tokens\":3,\"input_tokens_details\":{\"image_tokens\":1,\"text_tokens\":2},\"output_tokens\":4,\"total_tokens\":7}}\n\n")
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "OpenAI", Preset: "openai", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "gpt-image-1", []string{"image_edit"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstreamModel.ID, []string{"image_edit"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Streaming edits", Scopes: []string{"images:edit"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()
	body, contentType := imageForm(t, map[string]string{"model": model.ID, "prompt": "Add a hat", "stream": "true", "partial_images": "1"}, []imageUpload{{"image", "source.png", makePNG(t, 2, 2)}})
	status, responseType, responseBody := postImage(t, proxy.URL, "images/edits", secret, body, contentType)
	if status != http.StatusOK || responseType != "text/event-stream" || !bytes.Contains(responseBody, []byte("image_edit.completed")) {
		t.Fatalf("status=%d type=%q body=%s", status, responseType, responseBody)
	}
	var state, accounting string
	var inputTokens, outputTokens int64
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,input_tokens,output_tokens FROM attempts").Scan(&state, &accounting, &inputTokens, &outputTokens); err != nil || state != "succeeded" || accounting != "provider_reported" || inputTokens != 3 || outputTokens != 4 {
		t.Fatalf("attempt=%s/%s usage=%d/%d err=%v", state, accounting, inputTokens, outputTokens, err)
	}
}

func TestOpenAIImageVariationRequiresSquarePNG(t *testing.T) {
	var model, n string
	calls, fallbackCalls := 0, 0
	var failPrimary atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if failPrimary.Load() {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.URL.Path != "/v1/images/variations" {
			t.Errorf("upstream path = %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(maxImageMultipartBody); err != nil {
			t.Errorf("parse upstream multipart: %v", err)
			return
		}
		model, n = request.FormValue("model"), request.FormValue("n")
		io.WriteString(response, `{"created":1,"data":[{"b64_json":"eA=="}]}`)
	}))
	defer upstream.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fallbackCalls++
		io.WriteString(response, `{"created":1,"data":[]}`)
	}))
	defer fallback.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "dall-e-2", []string{"image_variation"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "variation fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback-variation", []string{"image_variation"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Image variations", Scopes: []string{"images:variation"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No variations", Scopes: []string{"images:edit"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body, contentType := imageForm(t, map[string]string{"model": publicModel.ID, "n": "2", "response_format": "b64_json"}, []imageUpload{{"image", "source.png", makePNG(t, 2, 2)}})
	status, _, _ := postImage(t, server.URL, "images/variations", secret, body, contentType)
	if status != http.StatusOK || model != "dall-e-2" || n != "2" || calls != 1 {
		t.Fatalf("status=%d model=%q n=%q calls=%d", status, model, n, calls)
	}
	var accounting string
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT usage_status FROM attempts WHERE state='succeeded'").Scan(&accounting); err != nil || accounting != "unknown" {
		t.Fatalf("accounting=%q err=%v", accounting, err)
	}
	invalidBody, invalidType := imageForm(t, map[string]string{"model": publicModel.ID}, []imageUpload{{"image", "source.png", makePNG(t, 2, 1)}})
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, invalidBody, invalidType); status != http.StatusBadRequest || calls != 1 {
		t.Fatalf("non-square status=%d calls=%d", status, calls)
	}
	if status, _, _ = postImage(t, server.URL, "images/variations", denied, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("denied status=%d calls=%d", status, calls)
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("lowest-cost status=%d calls=%d", status, calls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("free-only status=%d calls=%d", status, calls)
	}
	fallbackRoute, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	failPrimary.Store(true)
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, body, contentType); status != http.StatusBadGateway || calls != 2 || fallbackCalls != 0 {
		t.Fatalf("failure status=%d primary=%d fallback=%d", status, calls, fallbackCalls)
	}
	var failed int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed attempts=%d err=%v", failed, err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, fallbackRoute.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 100}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, body, contentType); status != http.StatusTooManyRequests || calls != 2 {
		t.Fatalf("token-policy status=%d calls=%d", status, calls)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "DELETE FROM limit_policies"); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postImage(t, server.URL, "images/variations", secret, body, contentType); status != http.StatusTooManyRequests || calls != 2 {
		t.Fatalf("spend-policy status=%d calls=%d", status, calls)
	}
}

func TestValidateImageMultipartRejectsInvalidForms(t *testing.T) {
	pngBody := makePNG(t, 2, 2)
	for name, limit := range map[string]int{"edit": maxImageEditFile, "variation": maxImageVariationFile} {
		t.Run(name+" exact file limit", func(t *testing.T) {
			body := append(append([]byte(nil), pngBody...), make([]byte, limit-len(pngBody))...)
			if _, _, err := readImageConfig(bytes.NewReader(body), int64(limit)); err == nil {
				t.Fatal("exact file limit was accepted")
			}
		})
	}
	validBody, validType := imageForm(t, map[string]string{"model": "image", "prompt": "combine", "n": "null", "output_compression": "null", "stream": "null", "partial_images": "null"}, []imageUpload{{"image[]", "first.png", pngBody}, {"image[]", "second.png", pngBody}, {"mask", "mask.png", pngBody}})
	if input, err := validateImageMultipart(validBody, validType, false); err != nil || input.model != "image" || input.n != 1 {
		t.Fatalf("valid multi-image edit: input=%+v err=%v", input, err)
	}
	variationBody, variationType := imageForm(t, map[string]string{"model": "image", "n": "null", "response_format": "null", "size": "null"}, []imageUpload{{"image", "source.png", pngBody}})
	if input, err := validateImageMultipart(variationBody, variationType, true); err != nil || input.model != "image" || input.n != 1 {
		t.Fatalf("valid nullable variation: input=%+v err=%v", input, err)
	}
	for name, test := range map[string]struct {
		fields    map[string]string
		uploads   []imageUpload
		variation bool
	}{
		"missing model":   {map[string]string{"prompt": "x"}, []imageUpload{{"image", "source.png", pngBody}}, false},
		"missing prompt":  {map[string]string{"model": "image"}, []imageUpload{{"image", "source.png", pngBody}}, false},
		"invalid n":       {map[string]string{"model": "image", "prompt": "x", "n": "11"}, []imageUpload{{"image", "source.png", pngBody}}, false},
		"bracketed model": {map[string]string{"model[]": "image", "prompt": "x"}, []imageUpload{{"image", "source.png", pngBody}}, false},
		"invalid image":   {map[string]string{"model": "image", "prompt": "x"}, []imageUpload{{"image", "source.png", []byte("not an image")}}, false},
		"mask mismatch":   {map[string]string{"model": "image", "prompt": "x"}, []imageUpload{{"image", "source.png", pngBody}, {"mask", "mask.png", makePNG(t, 1, 1)}}, false},
		"extra file":      {map[string]string{"model": "image", "prompt": "x"}, []imageUpload{{"image", "source.png", pngBody}, {"reference", "extra.png", pngBody}}, false},
		"variation mask":  {map[string]string{"model": "image"}, []imageUpload{{"image", "source.png", pngBody}, {"mask", "mask.png", pngBody}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			body, contentType := imageForm(t, test.fields, test.uploads)
			if _, err := validateImageMultipart(body, contentType, test.variation); err == nil {
				t.Fatal("invalid form was accepted")
			}
		})
	}
}

func TestImageMultipartRejectsBeforeReadingBody(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, allowed, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Allowed", Scopes: []string{"images:edit"}, ModelPatterns: []string{"*"}, ConnectionIDs: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Denied", Scopes: []string{"images:generate"}, ModelPatterns: []string{"*"}, ConnectionIDs: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	body, contentType := imageForm(t, map[string]string{"model": "image", "prompt": "edit"}, []imageUpload{{"image", "source.png", makePNG(t, 1, 1)}})

	for name, test := range map[string]struct {
		secret string
		busy   bool
		status int
	}{
		"missing scope": {denied, false, http.StatusNotFound},
		"busy pipeline": {allowed, true, http.StatusTooManyRequests},
	} {
		t.Run(name, func(t *testing.T) {
			if test.busy {
				handler.uploads <- struct{}{}
				defer handler.releaseMultipart()
			}
			reader := &countingReadCloser{Reader: bytes.NewReader(body)}
			request := httptest.NewRequest(http.MethodPost, "/api/openai/v1/images/edits", reader)
			request.Header.Set("Authorization", "Bearer "+test.secret)
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.status || reader.reads != 0 {
				t.Fatalf("status=%d reads=%d", response.Code, reader.reads)
			}
		})
	}
}

type countingReadCloser struct {
	io.Reader
	reads int
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	reader.reads++
	return reader.Reader.Read(buffer)
}

func (*countingReadCloser) Close() error { return nil }

type imageUpload struct {
	field, filename string
	body            []byte
}

func imageForm(t *testing.T, fields map[string]string, uploads []imageUpload) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, upload := range uploads {
		part, err := writer.CreateFormFile(upload.field, upload.filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(upload.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func makePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var body bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	value.Set(0, 0, color.White)
	if err := png.Encode(&body, value); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func postImage(t *testing.T, baseURL, path, key string, body []byte, contentType string) (int, string, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/openai/v1/"+path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", contentType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode, response.Header.Get("Content-Type"), mustRead(t, response.Body)
}
