package gateway

import (
	"bytes"
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

func TestOpenAIAudioTranscriptionPreservesMultipartAndRewritesModel(t *testing.T) {
	var upstreamModel, prompt, filename, authorization string
	var audio []byte
	calls, fallbackCalls := 0, 0
	var failPrimary, incompleteStream atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if failPrimary.Load() {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		authorization = request.Header.Get("Authorization")
		if request.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("upstream path = %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(maxAudioUploadBody); err != nil {
			t.Errorf("parse upstream multipart: %v", err)
			return
		}
		upstreamModel, prompt = request.FormValue("model"), request.FormValue("prompt")
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Errorf("read upstream file: %v", err)
			return
		}
		defer file.Close()
		filename = header.Filename
		audio, err = io.ReadAll(file)
		if err != nil {
			t.Errorf("read upstream audio: %v", err)
			return
		}
		if request.FormValue("stream") == "true" {
			response.Header().Set("Content-Type", "text/event-stream")
			if incompleteStream.Load() {
				io.WriteString(response, "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"partial\"}\n\n")
				return
			}
			io.WriteString(response, "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"hello\"}\n\nevent: transcript.text.done\ndata: {\"type\":\"transcript.text.done\",\"text\":\"hello\",\"usage\":{\"type\":\"tokens\",\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}\n\n")
			return
		}
		response.Header().Set("Content-Type", "application/json")
		io.WriteString(response, `{"text":"hello","usage":{"type":"tokens","input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fallbackCalls++
		io.WriteString(response, `{"text":"fallback"}`)
	}))
	defer fallback.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "whisper-upstream", []string{"audio_transcription"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "fallback-whisper", []string{"audio_transcription"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Transcription", Scopes: []string{"audio:transcribe"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No transcription", Scopes: []string{"chat:generate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body, contentType := audioForm(t, publicModel.ID, "recording.wav", []byte("RIFFaudio"), map[string]string{"prompt": "Names: Ada", "response_format": "json"})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/transcriptions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", contentType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := string(mustRead(t, response.Body)); response.StatusCode != http.StatusOK || got != `{"text":"hello","usage":{"type":"tokens","input_tokens":3,"output_tokens":2,"total_tokens":5}}` {
		t.Fatalf("status=%d body=%s", response.StatusCode, got)
	}
	if upstreamModel != "whisper-upstream" || prompt != "Names: Ada" || filename != "recording.wav" || string(audio) != "RIFFaudio" || authorization != "Bearer provider-secret" {
		t.Fatalf("model=%q prompt=%q filename=%q audio=%q auth=%q", upstreamModel, prompt, filename, audio, authorization)
	}
	var accounting string
	var inputTokens, outputTokens int64
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,input_tokens,output_tokens FROM attempts WHERE state='succeeded'").Scan(&accounting, &inputTokens, &outputTokens); err != nil || accounting != "provider_reported" || inputTokens != 3 || outputTokens != 2 {
		t.Fatalf("accounting=%q input=%d output=%d err=%v", accounting, inputTokens, outputTokens, err)
	}
	streamBody, streamType := audioForm(t, publicModel.ID, "recording.wav", []byte("RIFFaudio"), map[string]string{"stream": "true"})
	if status := postAudio(t, server.URL, secret, streamBody, streamType); status != http.StatusOK {
		t.Fatalf("stream status=%d", status)
	}
	var succeeded int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='succeeded' AND usage_status='provider_reported' AND input_tokens=3 AND output_tokens=2").Scan(&succeeded); err != nil || succeeded != 2 {
		t.Fatalf("settled streams=%d err=%v", succeeded, err)
	}
	incompleteStream.Store(true)
	if status := postAudio(t, server.URL, secret, streamBody, streamType); status != http.StatusOK {
		t.Fatalf("incomplete stream status=%d", status)
	}
	incompleteStream.Store(false)
	var failed int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("incomplete attempts=%d err=%v", failed, err)
	}
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/api/openai/v1/audio/transcriptions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+denied)
	request.Header.Set("Content-Type", contentType)
	deniedResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	deniedResponse.Body.Close()
	if deniedResponse.StatusCode != http.StatusNotFound || calls != 3 {
		t.Fatalf("denied status=%d upstream calls=%d", deniedResponse.StatusCode, calls)
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusNotFound || calls != 3 {
		t.Fatalf("lowest-cost status=%d upstream calls=%d", status, calls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusNotFound || calls != 3 {
		t.Fatalf("free-only status=%d upstream calls=%d", status, calls)
	}
	fixed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, fixed.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	failPrimary.Store(true)
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusBadGateway || calls != 4 || fallbackCalls != 0 {
		t.Fatalf("failed status=%d primary=%d fallback=%d", status, calls, fallbackCalls)
	}
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 2 {
		t.Fatalf("failed attempts=%d err=%v", failed, err)
	}
	failPrimary.Store(false)
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "body_bytes", Algorithm: "ceiling", LimitUnits: int64(len(body) - 1)}); err != nil {
		t.Fatal(err)
	}
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusTooManyRequests || calls != 4 {
		t.Fatalf("body limit status=%d upstream calls=%d", status, calls)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "DELETE FROM limit_policies"); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "output_tokens", Algorithm: "ceiling", LimitUnits: 100}); err != nil {
		t.Fatal(err)
	}
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusTooManyRequests || calls != 4 {
		t.Fatalf("output limit status=%d upstream calls=%d", status, calls)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "DELETE FROM limit_policies"); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"}); err != nil {
		t.Fatal(err)
	}
	if status := postAudio(t, server.URL, secret, body, contentType); status != http.StatusTooManyRequests || calls != 4 {
		t.Fatalf("spend limit status=%d upstream calls=%d", status, calls)
	}
}

func TestOpenAIAudioTranslationPreservesMultipartAndRejectsStreaming(t *testing.T) {
	var model, prompt string
	calls, fallbackCalls := 0, 0
	var failPrimary atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if failPrimary.Load() {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.URL.Path != "/v1/audio/translations" {
			t.Errorf("upstream path = %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(maxAudioUploadBody); err != nil {
			t.Errorf("parse upstream multipart: %v", err)
			return
		}
		model, prompt = request.FormValue("model"), request.FormValue("prompt")
		response.Header().Set("Content-Type", "text/plain")
		io.WriteString(response, "hello")
	}))
	defer upstream.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fallbackCalls++
		io.WriteString(response, "fallback")
	}))
	defer fallback.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, publicModel := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "whisper-1", []string{"audio_translation"})
	fallbackConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "translation fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err = providerService.PutCredential(ctx, owner, fallbackConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := providerService.CreateUpstreamModel(ctx, owner, fallbackConnection.ID, "provider-translation", []string{"audio_translation"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Translation", Scopes: []string{"audio:translate"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID, fallbackConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No translation", Scopes: []string{"audio:transcribe"}, ModelPatterns: []string{publicModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body, contentType := audioForm(t, publicModel.ID, "recording.wav", []byte("RIFFaudio"), map[string]string{"prompt": "Product names", "response_format": "text"})
	status, responseType, responseBody := postAudioAt(t, server.URL, "audio/translations", secret, body, contentType)
	if status != http.StatusOK || responseType != "text/plain" || string(responseBody) != "hello" || model != "whisper-1" || prompt != "Product names" || calls != 1 {
		t.Fatalf("status=%d type=%q body=%q model=%q prompt=%q calls=%d", status, responseType, responseBody, model, prompt, calls)
	}
	var state, accounting string
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status FROM attempts").Scan(&state, &accounting); err != nil || state != "succeeded" || accounting != "unknown" {
		t.Fatalf("state=%q accounting=%q err=%v", state, accounting, err)
	}
	streamBody, streamType := audioForm(t, publicModel.ID, "recording.wav", []byte("RIFFaudio"), map[string]string{"stream": "true"})
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, streamBody, streamType); status != http.StatusBadRequest || calls != 1 {
		t.Fatalf("stream status=%d upstream calls=%d", status, calls)
	}
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", denied, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("denied status=%d upstream calls=%d", status, calls)
	}
	routed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, publicModel.Revision, providers.RouteConfigInput{Strategy: "lowest_cost", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("lowest-cost status=%d upstream calls=%d", status, calls)
	}
	freeOnly, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, routed.Revision, providers.RouteConfigInput{Strategy: "fixed", FreeOnly: true, Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, body, contentType); status != http.StatusNotFound || calls != 1 {
		t.Fatalf("free-only status=%d upstream calls=%d", status, calls)
	}
	fixed, err := providerService.ConfigureRoute(ctx, owner, publicModel.ID, freeOnly.Revision, providers.RouteConfigInput{Strategy: "fixed", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerService.ConfigureRoute(ctx, owner, publicModel.ID, fixed.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: publicModel.TargetModelID, Priority: 1, Enabled: true}, {UpstreamModelID: fallbackModel.ID, Priority: 2, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	failPrimary.Store(true)
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, body, contentType); status != http.StatusBadGateway || calls != 2 || fallbackCalls != 0 {
		t.Fatalf("failure status=%d primary=%d fallback=%d", status, calls, fallbackCalls)
	}
	var failed int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE state='failed' AND usage_status='unknown'").Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed attempts=%d err=%v", failed, err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 100}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, body, contentType); status != http.StatusTooManyRequests || calls != 2 {
		t.Fatalf("token-policy status=%d upstream calls=%d", status, calls)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "DELETE FROM limit_policies"); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = postAudioAt(t, server.URL, "audio/translations", secret, body, contentType); status != http.StatusTooManyRequests || calls != 2 {
		t.Fatalf("spend-policy status=%d upstream calls=%d", status, calls)
	}
}

func TestValidateAudioMultipartRejectsInvalidInputs(t *testing.T) {
	validBody, validType := audioForm(t, "model", "clip.mp3", []byte("audio"), nil)
	if model, stream, err := validateAudioMultipart(validBody, validType); err != nil || model != "model" || stream {
		t.Fatalf("valid multipart: model=%q stream=%t err=%v", model, stream, err)
	}
	nullStreamBody, nullStreamType := audioForm(t, "model", "clip.mp3", []byte("audio"), map[string]string{"stream": "null"})
	if _, stream, err := validateAudioMultipart(nullStreamBody, nullStreamType); err != nil || stream {
		t.Fatalf("null stream: stream=%t err=%v", stream, err)
	}
	wrongExtensionBody, wrongExtensionType := audioForm(t, "model", "clip.txt", []byte("audio"), nil)
	emptyFileBody, emptyFileType := audioForm(t, "model", "clip.mp3", nil, nil)
	missingModelBody, missingModelType := audioForm(t, "", "clip.mp3", []byte("audio"), nil)
	extraFileBody, extraFileType := audioFormWithExtraFile(t)
	for name, test := range map[string]struct {
		body        []byte
		contentType string
	}{
		"content type":  {validBody, "application/json"},
		"extension":     {wrongExtensionBody, wrongExtensionType},
		"empty file":    {emptyFileBody, emptyFileType},
		"missing model": {missingModelBody, missingModelType},
		"extra file":    {extraFileBody, extraFileType},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := validateAudioMultipart(test.body, test.contentType); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	for name, raw := range map[string]string{
		"incomplete":  "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"partial\"}\n\n",
		"typed error": "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n",
	} {
		if err := validateAudioTranscriptionStream("text/event-stream", []byte(raw)); err == nil {
			t.Fatalf("%s stream should fail", name)
		}
	}
}

func TestTailCaptureWrapsWithoutLosingOrder(t *testing.T) {
	short := tailCapture{limit: 1024}
	_, _ = short.Write([]byte("abc"))
	if len(short.data) != 3 || cap(short.data) >= short.limit {
		t.Fatalf("short capture reserved too much: len=%d cap=%d", len(short.data), cap(short.data))
	}
	capture := tailCapture{limit: 5}
	_, _ = capture.Write([]byte("abc"))
	_, _ = capture.Write([]byte("def"))
	if got := string(capture.Bytes()); got != "bcdef" {
		t.Fatalf("first wrap = %q", got)
	}
	_, _ = capture.Write([]byte("gh"))
	if got := string(capture.Bytes()); got != "defgh" {
		t.Fatalf("second wrap = %q", got)
	}
	_, _ = capture.Write([]byte("0123456789"))
	if got := string(capture.Bytes()); got != "56789" {
		t.Fatalf("oversized write = %q", got)
	}
}

func audioForm(t *testing.T, model, filename string, audio []byte, fields map[string]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		_ = writer.WriteField("model", model)
	}
	for name, value := range fields {
		_ = writer.WriteField(name, value)
	}
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func mustRead(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	value, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func audioFormWithExtraFile(t *testing.T) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "model")
	for _, name := range []string{"file", "reference"} {
		part, err := writer.CreateFormFile(name, name+".wav")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("audio"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func postAudio(t *testing.T, baseURL, key string, body []byte, contentType string) int {
	t.Helper()
	status, _, _ := postAudioAt(t, baseURL, "audio/transcriptions", key, body, contentType)
	return status
}

func postAudioAt(t *testing.T, baseURL, path, key string, body []byte, contentType string) (int, string, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/openai/v1/"+path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", contentType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := mustRead(t, response.Body)
	response.Body.Close()
	return response.StatusCode, response.Header.Get("Content-Type"), responseBody
}
