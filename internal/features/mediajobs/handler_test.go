package mediajobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestReplicateMediaJobPersistsPollsAccountsAndProtectsContent(t *testing.T) {
	var polls atomic.Int64
	var authorization, createPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/models/acme/video/predictions":
			createPath = request.URL.Path
			body, _ := io.ReadAll(request.Body)
			if !bytes.Contains(body, []byte(`"prompt":"private prompt"`)) {
				t.Errorf("create body = %s", body)
			}
			_, _ = io.WriteString(response, `{"id":"pred_1","status":"starting","output":null}`)
		case "/v1/predictions/pred_1":
			polls.Add(1)
			_, _ = io.WriteString(response, `{"id":"pred_1","status":"succeeded","output":["https://cdn.example.test/result.mp4"]}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()

	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	owner, ownerToken, err := authService.Claim(ctx, auth.ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{7}, 32)
	providerService := providers.New(store.SystemDB(), masterKey)
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "Replicate", Preset: "replicate", Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", connection.ID); err != nil {
		t.Fatal(err)
	}
	upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "acme/video", []string{"media_jobs"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "video-maker", "Video maker", "", upstreamModel.ID, []string{"media_jobs"})
	if err != nil {
		t.Fatal(err)
	}
	keyService := keys.New(store.SystemDB())
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Media", Scopes: []string{Scope}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	usageService := usage.New(store.SystemDB())
	service := New(store.SystemDB(), keyService, providerService, usageService, masterKey)
	mux := http.NewServeMux()
	NewHandler(service, keyService, auth.NewHandler(authService)).Register(mux)
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(workerCtx) }()
	defer func() { stopWorker(); <-done }()

	created := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs", `{"model":"video-maker","media_type":"video","input":{"prompt":"private prompt"}}`)
	if created.Code != http.StatusAccepted || created.Header().Get("X-Pocket-AI-Request-ID") == "" {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	var createBody struct {
		Job Job `json:"job"`
	}
	if json.Unmarshal(created.Body.Bytes(), &createBody) != nil || createBody.Job.ID == "" {
		t.Fatalf("create body=%s", created.Body.String())
	}
	deadline := time.Now().Add(4 * time.Second)
	var result Job
	for time.Now().Before(deadline) {
		got := performRequest(t, mux, secret, http.MethodGet, "/api/v1/media/jobs/"+createBody.Job.ID, "")
		var body struct {
			Job Job `json:"job"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &body)
		result = body.Job
		if result.State == "succeeded" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if result.State != "succeeded" || !strings.Contains(string(result.Output), "result.mp4") || authorization != "Bearer provider-secret" || createPath == "" || polls.Load() == 0 {
		t.Fatalf("job=%#v auth=%q path=%q polls=%d", result, authorization, createPath, polls.Load())
	}
	var inputCiphertext, inputNonce []byte
	var attemptState, usageStatus string
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT input_ciphertext,input_nonce FROM media_jobs WHERE id=?`, result.ID).Scan(&inputCiphertext, &inputNonce); err != nil || inputCiphertext != nil || inputNonce != nil {
		t.Fatalf("terminal input was retained: ciphertext=%d nonce=%d err=%v", len(inputCiphertext), len(inputNonce), err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status FROM attempts WHERE request_id=?`, result.RequestID).Scan(&attemptState, &usageStatus); err != nil || attemptState != "succeeded" || usageStatus != "unknown" {
		t.Fatalf("accounting state=%q usage=%q err=%v", attemptState, usageStatus, err)
	}

	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/media/jobs/"+result.ID, nil)
	adminRequest.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: ownerToken})
	adminResponse := httptest.NewRecorder()
	mux.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK || strings.Contains(adminResponse.Body.String(), "result.mp4") {
		t.Fatalf("admin response status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{Scope}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	other := performRequest(t, mux, otherSecret, http.MethodGet, "/api/v1/media/jobs/"+result.ID, "")
	if other.Code != http.StatusNotFound {
		t.Fatalf("other key status=%d body=%s", other.Code, other.Body.String())
	}
}

func TestQueuedMediaJobCancellationIsIdempotentAndReleasesAdmission(t *testing.T) {
	ctx, store, owner, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	mux := http.NewServeMux()
	NewHandler(service, keyService, auth.NewHandler(auth.New(store.SystemDB()))).Register(mux)
	created := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs", `{"model":"media-model","media_type":"image","input":{"prompt":"secret"}}`)
	var body struct {
		Job Job `json:"job"`
	}
	if created.Code != http.StatusAccepted || json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	for index := 0; index < 2; index++ {
		canceled := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs/"+body.Job.ID+"/cancel", `{}`)
		if canceled.Code != http.StatusOK || !strings.Contains(canceled.Body.String(), `"state":"canceled"`) {
			t.Fatalf("cancel %d status=%d body=%s", index, canceled.Code, canceled.Body.String())
		}
	}
	var requestState string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state FROM requests WHERE id=?", body.Job.RequestID).Scan(&requestState); err != nil || requestState != "cancelled" {
		t.Fatalf("request state=%q err=%v owner=%s", requestState, err, owner.ID)
	}
}

func TestTogetherVideoCancellationIsLocalAndStopsPolling(t *testing.T) {
	var creates, gets, cancels atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos":
			creates.Add(1)
			body, _ := io.ReadAll(request.Body)
			if !bytes.Contains(body, []byte(`"model":"acme/video"`)) {
				t.Errorf("Together model was not normalized: %s", body)
			}
			_, _ = io.WriteString(response, `{"id":"vid_1","status":"queued"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/vid_1":
			gets.Add(1)
			_, _ = io.WriteString(response, `{"id":"vid_1","status":"in_progress"}`)
		default:
			cancels.Add(1)
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "together", "acme/video", "video")
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE preset='together'", upstream.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(service, keyService, auth.NewHandler(auth.New(store.SystemDB()))).Register(mux)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	created := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs", `{"model":"media-model","media_type":"video","input":{"prompt":"movie"}}`)
	var body struct {
		Job Job `json:"job"`
	}
	if created.Code != http.StatusAccepted || json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(ctx, mustPrincipal(t, keyService, secret), body.Job.ID)
		if err == nil && job.ProviderJobID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	first := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs/"+body.Job.ID+"/cancel", `{}`)
	if first.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", first.Code, first.Body.String())
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := service.Get(ctx, mustPrincipal(t, keyService, secret), body.Job.ID)
		if job.State == "canceled" {
			if creates.Load() != 1 || cancels.Load() != 0 {
				t.Fatalf("Together calls create=%d get=%d unexpected=%d", creates.Load(), gets.Load(), cancels.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Together job was not locally canceled")
}

func TestGeminiVeoMediaJobNormalizesPollsAndAccounts(t *testing.T) {
	var creates, polls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Header.Get("x-goog-api-key") != "secret" {
			t.Errorf("provider key=%q", request.Header.Get("x-goog-api-key"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1beta/models/veo-3.1-generate-preview:predictLongRunning":
			creates.Add(1)
			body, _ := io.ReadAll(request.Body)
			if !bytes.Contains(body, []byte(`"instances":[{"prompt":"movie"}]`)) || !bytes.Contains(body, []byte(`"aspectRatio":"16:9"`)) {
				t.Errorf("Gemini video body=%s", body)
			}
			_, _ = io.WriteString(response, `{"name":"operations/video_1","done":false}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1beta/operations/video_1":
			polls.Add(1)
			_, _ = io.WriteString(response, `{"name":"operations/video_1","done":true,"response":{"generatedVideos":[{"video":{"uri":"https://files.example.test/video.mp4"}}]}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "gemini", "veo-3.1-generate-preview", "video")
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE preset='gemini'", upstream.URL+"/v1beta"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(service, keyService, auth.NewHandler(auth.New(store.SystemDB()))).Register(mux)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	created := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs", `{"model":"media-model","media_type":"video","input":{"prompt":"movie","parameters":{"aspectRatio":"16:9"}}}`)
	var body struct {
		Job Job `json:"job"`
	}
	if created.Code != http.StatusAccepted || json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	principal := mustPrincipal(t, keyService, secret)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(ctx, principal, body.Job.ID)
		if err == nil && job.State == "succeeded" {
			if !strings.Contains(string(job.Output), "video.mp4") || job.ProviderJobID != "operations/video_1" || creates.Load() != 1 || polls.Load() == 0 {
				t.Fatalf("job=%#v creates=%d polls=%d", job, creates.Load(), polls.Load())
			}
			var state, status, operation string
			if err = store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,target_operation FROM attempts WHERE request_id=?", job.RequestID).Scan(&state, &status, &operation); err != nil || state != "succeeded" || status != "unknown" || operation != "predictLongRunning" {
				t.Fatalf("attempt=%s/%s operation=%s err=%v", state, status, operation, err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Gemini video job did not complete")
}

func TestReplicateRunningJobUsesProviderCancel(t *testing.T) {
	var canceled atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/models/acme/image/predictions":
			_, _ = io.WriteString(response, `{"id":"pred_cancel","status":"processing","output":null}`)
		case "/v1/predictions/pred_cancel":
			_, _ = io.WriteString(response, `{"id":"pred_cancel","status":"processing","output":null}`)
		case "/v1/predictions/pred_cancel/cancel":
			canceled.Add(1)
			_, _ = io.WriteString(response, `{"id":"pred_cancel","status":"canceled","output":null}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	_, _ = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE preset='replicate'", upstream.URL+"/v1")
	mux := http.NewServeMux()
	NewHandler(service, keyService, auth.NewHandler(auth.New(store.SystemDB()))).Register(mux)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	created := performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs", `{"model":"media-model","media_type":"image","input":{"prompt":"image"}}`)
	var body struct {
		Job Job `json:"job"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &body)
	principal := mustPrincipal(t, keyService, secret)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := service.Get(ctx, principal, body.Job.ID)
		if job.ProviderJobID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	performRequest(t, mux, secret, http.MethodPost, "/api/v1/media/jobs/"+body.Job.ID+"/cancel", `{}`)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := service.Get(ctx, principal, body.Job.ID)
		if job.State == "canceled" {
			if canceled.Load() != 1 {
				t.Fatalf("provider cancel calls=%d", canceled.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Replicate job was not canceled")
}

func TestRestartMarksDispatchedSubmissionUnknownAndClosesAccounting(t *testing.T) {
	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	principal := mustPrincipal(t, keyService, secret)
	job, err := service.Create(ctx, principal, CreateInput{Model: "media-model", MediaType: "image", Input: json.RawMessage(`{"prompt":"private"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE media_jobs SET state='submitting' WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.usage.MarkDispatching(ctx, func() string {
		var id string
		_ = store.SystemDB().QueryRowContext(ctx, "SELECT attempt_id FROM media_jobs WHERE id=?", job.ID).Scan(&id)
		return id
	}()); err != nil {
		t.Fatal(err)
	}
	restarted := New(store.SystemDB(), keyService, service.providers, service.usage, service.masterKey)
	restarted.recoverInterrupted(ctx)
	recovered, err := restarted.Get(ctx, principal, job.ID)
	if err != nil || recovered.State != "interrupted_unknown" || !strings.Contains(string(recovered.Error), "submission_outcome_unknown") {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	var attemptState string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state FROM attempts WHERE request_id=?", job.RequestID).Scan(&attemptState); err != nil || attemptState != "interrupted_unknown" {
		t.Fatalf("attempt state=%q err=%v", attemptState, err)
	}
}

func TestProviderPollErrorClassification(t *testing.T) {
	for _, test := range []struct {
		err       error
		retryable bool
	}{{providerHTTPError(404), false}, {providerHTTPError(429), true}, {providerHTTPError(503), true}, {errors.New("invalid JSON"), false}} {
		if retryableProviderError(test.err) != test.retryable {
			t.Fatalf("retryableProviderError(%v)=%t, want %t", test.err, retryableProviderError(test.err), test.retryable)
		}
	}
}

func TestProviderIdentifiersRejectPathTraversal(t *testing.T) {
	if safeSegment("..") || safeProviderPath("operations/../admin") {
		t.Fatal("provider identifiers accepted path traversal")
	}
}

func TestPermanentProviderPollErrorTerminatesJob(t *testing.T) {
	var polls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPost {
			_, _ = io.WriteString(response, `{"id":"pred_missing","status":"starting","output":null}`)
			return
		}
		polls.Add(1)
		http.NotFound(response, request)
	}))
	defer upstream.Close()

	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	principal := mustPrincipal(t, keyService, secret)
	job, err := service.Create(ctx, principal, CreateInput{Model: "media-model", MediaType: "image", Input: json.RawMessage(`{"prompt":"private"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", upstream.URL+"/v1", job.connectionID); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := service.Get(ctx, principal, job.ID)
		if getErr == nil && current.State == "failed" {
			if polls.Load() != 1 || !strings.Contains(string(current.Error), "provider_poll_failed") {
				t.Fatalf("job=%#v polls=%d", current, polls.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("media job did not terminate after permanent poll failure")
}

func TestCustomScriptedMediaJobTransformsCreateAndPoll(t *testing.T) {
	var creates, gets atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/jobs":
			creates.Add(1)
			if request.URL.Query().Get("kind") != "image" {
				t.Errorf("custom create query = %q", request.URL.RawQuery)
			}
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPost || !bytes.Contains(body, []byte(`"prompt":"draw"`)) {
				t.Errorf("custom create method=%s body=%s", request.Method, body)
			}
			_, _ = io.WriteString(response, `{"job_id":"custom/1","phase":"waiting"}`)
		case "/api/jobs/custom/1":
			gets.Add(1)
			if request.URL.EscapedPath() != "/api/jobs/custom%2F1" {
				t.Errorf("custom poll escaped path = %q", request.URL.EscapedPath())
			}
			_, _ = io.WriteString(response, `{"job_id":"custom/1","phase":"done","asset":"https://cdn.example.test/custom.png"}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	target := providers.Target{
		PublicModel: providers.PublicModel{UpstreamID: "custom-image-v1"}, BaseURL: upstream.URL + "/api", AllowPrivateNetwork: true, TimeoutMS: 5_000, Credential: "secret",
		AdapterRequestScript: `function (request) {
			if (request.body.action === "get") return { method: "GET", path: "jobs/" + encodeURIComponent(request.body.id), body: {} };
			return { method: "POST", path: "jobs?kind=image", body: request.body };
		}`,
		AdapterResponseScript: `function (response) {
			return { body: { id: response.body.job_id, status: response.body.phase === "done" ? "succeeded" : "queued", output: response.body.asset || null } };
		}`,
	}
	created, err := createCustomMediaJob(context.Background(), target, json.RawMessage(`{"prompt":"draw"}`))
	if err != nil || created.ID != "custom/1" || created.Status != "queued" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	polled, err := getCustomMediaJob(context.Background(), target, created.ID)
	if err != nil || polled.Status != "succeeded" || !strings.Contains(string(polled.Output), "custom.png") || creates.Load() != 1 || gets.Load() != 1 {
		t.Fatalf("polled=%#v create=%d get=%d err=%v", polled, creates.Load(), gets.Load(), err)
	}
}

func TestMediaJobRejectsUnknownPriceUnderSpendPolicy(t *testing.T) {
	ctx, store, owner, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	principal := mustPrincipal(t, keyService, secret)
	if _, err := service.usage.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: principal.KeyID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Create(ctx, principal, CreateInput{Model: "media-model", MediaType: "image", Input: json.RawMessage(`{"prompt":"private"}`)})
	var denial *usage.Denial
	if !errors.As(err, &denial) || denial.Metric != "spend" {
		t.Fatalf("create with spend policy = %v", err)
	}
	var count int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM media_jobs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied media jobs=%d err=%v", count, err)
	}
}

func TestMediaJobRequiresFixedRoute(t *testing.T) {
	ctx, store, _, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE public_models SET routing_strategy='weighted' WHERE id='media-model'"); err != nil {
		t.Fatal(err)
	}
	_, err := service.Create(ctx, mustPrincipal(t, keyService, secret), CreateInput{Model: "media-model", MediaType: "image", Input: json.RawMessage(`{"prompt":"private"}`)})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("create with weighted route = %v", err)
	}
}

func mediaFixture(t *testing.T, preset, upstreamID, mediaType string) (context.Context, *storage.Store, auth.User, *providers.Service, *keys.Service, *Service, string) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.New(store.SystemDB())
	owner, _, err := authService.Claim(ctx, auth.ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{8}, 32)
	providerService := providers.New(store.SystemDB(), masterKey)
	connection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: preset, Preset: preset, Enabled: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := providerService.PutCredential(ctx, owner, connection.ID, "secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, []string{"media_jobs"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := providerService.CreatePublicModel(ctx, owner, "media-model", "Media", "", upstream.ID, []string{"media_jobs"})
	if err != nil {
		t.Fatal(err)
	}
	keyService := keys.New(store.SystemDB())
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Media", Scopes: []string{Scope}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	usageService := usage.New(store.SystemDB())
	return ctx, store, owner, providerService, keyService, New(store.SystemDB(), keyService, providerService, usageService, masterKey), secret
}

func performRequest(t *testing.T, handler http.Handler, secret, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mustPrincipal(t *testing.T, service *keys.Service, secret string) keys.Principal {
	t.Helper()
	principal, err := service.Authenticate(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
