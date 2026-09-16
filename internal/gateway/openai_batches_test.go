package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestOpenAIBatchCreateRetrieveListAndCancel(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batches", Scopes: []string{"batches:manage", "files:manage", "responses:generate"}, ModelPatterns: []string{"assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{"batches:manage", "files:manage", "responses:generate"}, ModelPatterns: []string{"assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{8}, 32)
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, masterKey).Register(mux)
	content := []byte("{\"custom_id\":\"one\",\"method\":\"POST\",\"url\":\"/v1/responses\",\"body\":{\"model\":\"assistant\",\"input\":\"first\"}}\n{\"custom_id\":\"two\",\"method\":\"POST\",\"url\":\"/v1/responses\",\"body\":{\"model\":\"assistant\",\"input\":\"second\",\"store\":false}}\n")
	uploaded := performFileUpload(t, mux, secret, "batch.jsonl", content, map[string]string{"purpose": "batch"}, nil)
	if uploaded.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	var file openAIFile
	if err := json.Unmarshal(uploaded.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	created := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/responses","completion_window":"24h","metadata":{"suite":"gateway"},"output_expires_after":{"anchor":"created_at","seconds":3600}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var batch map[string]any
	if json.Unmarshal(created.Body.Bytes(), &batch) != nil || !strings.HasPrefix(batch["id"].(string), "batch_") || batch["status"] != "in_progress" || batch["model"] != "assistant" || batch["input_file_id"] != file.ID {
		t.Fatalf("created=%s", created.Body.String())
	}
	id := batch["id"].(string)
	var requestBytes, reserved int64
	var ciphertext, nonce []byte
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT request_bytes,request_ciphertext,request_nonce,reserved_result_bytes FROM openai_batch_items WHERE batch_id=? AND ordinal=1`, id).Scan(&requestBytes, &ciphertext, &nonce, &reserved); err != nil {
		t.Fatal(err)
	}
	if requestBytes == 0 || bytes.Contains(ciphertext, []byte(`"input":"first"`)) || len(ciphertext) != int(requestBytes)+16 || len(nonce) != 12 || reserved != openAIBatchResultReservation(2) {
		t.Fatalf("stored request bytes=%d ciphertext=%d nonce=%d reserve=%d", requestBytes, len(ciphertext), len(nonce), reserved)
	}
	if got := performOpenAIBatchRequest(t, mux, http.MethodGet, "/api/openai/v1/batches/"+id, otherSecret, ""); got.Code != http.StatusNotFound {
		t.Fatalf("cross-key status=%d body=%s", got.Code, got.Body.String())
	}
	listed := performOpenAIBatchRequest(t, mux, http.MethodGet, "/api/openai/v1/batches?limit=1", secret, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), id) || !strings.Contains(listed.Body.String(), `"object":"list"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	cancelled := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches/"+id+"/cancel", secret, "")
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) || !strings.Contains(cancelled.Body.String(), `"error_file_id":"file_`) {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	var remaining int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_batch_items WHERE batch_id=?`, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining items=%d err=%v", remaining, err)
	}
	_ = key
}

func TestOpenAIChatBatchUsesSharedDispatchAndAccounting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(response, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		if bytes.Contains(body, []byte(`"background"`)) || !bytes.Contains(body, []byte(`"store":false`)) || !bytes.Contains(body, []byte(`"stream":false`)) {
			http.Error(response, "invalid normalized request", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"id":"chat_provider","object":"chat.completion","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}}`)
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "chat-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Chat Batch", Scopes: []string{"batches:manage", "files:manage", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{7}, 32))
	mux := http.NewServeMux()
	handler.Register(mux)
	input := `{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":{"model":"` + model.ID + `","messages":[{"role":"user","content":"Hi"}],"max_tokens":8}}` + "\n"
	uploaded := performFileUpload(t, mux, secret, "chat-batch.jsonl", []byte(input), map[string]string{"purpose": "batch"}, nil)
	var file openAIFile
	if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
		t.Fatalf("upload=%d %s", uploaded.Code, uploaded.Body.String())
	}
	created := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/chat/completions","completion_window":"24h"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	job, ok := handler.claimOpenAIBatch(ctx)
	if !ok || job.endpoint != "/v1/chat/completions" {
		t.Fatalf("claimed=%v endpoint=%q", ok, job.endpoint)
	}
	handler.runOpenAIBatch(ctx, job)
	batch, err := handler.loadOpenAIBatch(ctx, job.keyID, job.batchID)
	if err != nil || batch.status != "completed" || !batch.usageKnown || batch.inputTokens != 4 || batch.outputTokens != 2 || batch.cachedTokens != 1 || batch.reasoningTokens != 1 || !batch.outputFileID.Valid {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	_, output, err := handler.loadOpenAIFileContent(ctx, job.keyID, batch.outputFileID.String)
	if err != nil || !bytes.Contains(output, []byte(`"object":"chat.completion"`)) || !bytes.Contains(output, []byte(`"model":"`+model.ID+`"`)) {
		t.Fatalf("output=%s err=%v", output, err)
	}
	requests, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
	if err != nil || len(requests) != 1 || requests[0].Operation != "chat/completions" || requests[0].Dialect != "openai" || requests[0].State != "succeeded" {
		t.Fatalf("requests=%#v err=%v", requests, err)
	}
}

func TestOpenAIBatchListUsesKeysetPagination(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batches", Scopes: []string{"batches:manage", "files:manage", "responses:generate"}, ModelPatterns: []string{"assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{4}, 32)).Register(mux)
	uploaded := performFileUpload(t, mux, secret, "batch.jsonl", []byte("{\"custom_id\":\"one\",\"method\":\"POST\",\"url\":\"/v1/responses\",\"body\":{\"model\":\"assistant\",\"input\":\"x\"}}\n"), map[string]string{"purpose": "batch"}, nil)
	var file openAIFile
	_ = json.Unmarshal(uploaded.Body.Bytes(), &file)
	ids := make([]string, 0, 3)
	for range 3 {
		created := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/responses","completion_window":"24h"}`)
		if created.Code != http.StatusOK {
			t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
		}
		var batch struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &batch)
		ids = append(ids, batch.ID)
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_batches SET created_at=?,in_progress_at=?,expires_at=?,retention_expires_at=?`, now, now, now+openAIBatchProcessingWindow.Milliseconds(), now+openAIBatchRetention.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	first := performOpenAIBatchRequest(t, mux, http.MethodGet, "/api/openai/v1/batches?limit=1", secret, "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), ids[0]) || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("first status=%d body=%s ids=%v", first.Code, first.Body.String(), ids)
	}
	second := performOpenAIBatchRequest(t, mux, http.MethodGet, "/api/openai/v1/batches?limit=1&after="+url.QueryEscape(ids[0]), secret, "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), ids[1]) || strings.Contains(second.Body.String(), ids[0]) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	for _, path := range []string{"/api/openai/v1/batches?limit=0", "/api/openai/v1/batches?limit=101", "/api/openai/v1/batches?after=missing", "/api/openai/v1/batches?after=a&after=b", "/api/openai/v1/batches?order=asc"} {
		result := performOpenAIBatchRequest(t, mux, http.MethodGet, path, secret, "")
		if result.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
}

func TestOpenAIBatchValidationScopesAndOwnership(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	masterKey := bytes.Repeat([]byte{6}, 32)
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, masterKey).Register(mux)
	makeKey := func(label string, scopes []string) string {
		_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: label, Scopes: scopes, ModelPatterns: []string{"assistant"}})
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	secret := makeKey("All", []string{"batches:manage", "files:manage", "responses:generate"})
	other := makeKey("Other", []string{"batches:manage", "files:manage", "responses:generate"})
	noChat := makeKey("No Chat", []string{"batches:manage", "files:manage", "responses:generate"})
	missingScopes := []string{
		makeKey("No Batch", []string{"files:manage", "responses:generate"}),
		makeKey("No Responses", []string{"batches:manage", "files:manage"}),
	}
	fileFor := func(key, content string) string {
		created := performFileUpload(t, mux, key, "batch.jsonl", []byte(content), map[string]string{"purpose": "batch"}, nil)
		if created.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", created.Code, created.Body.String())
		}
		var file openAIFile
		_ = json.Unmarshal(created.Body.Bytes(), &file)
		return file.ID
	}
	validLine := `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x"}}`
	validFile := fileFor(secret, validLine+"\n")
	if denied := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", noChat, `{"input_file_id":"`+validFile+`","endpoint":"/v1/chat/completions","completion_window":"24h"}`); denied.Code != http.StatusForbidden {
		t.Fatalf("missing chat scope status=%d body=%s", denied.Code, denied.Body.String())
	}
	for _, scopedSecret := range missingScopes {
		if denied := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", scopedSecret, `{"input_file_id":"`+validFile+`","endpoint":"/v1/responses","completion_window":"24h"}`); denied.Code != http.StatusForbidden {
			t.Fatalf("missing scopes status=%d body=%s", denied.Code, denied.Body.String())
		}
	}
	otherFile := fileFor(other, validLine+"\n")
	if isolated := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+otherFile+`","endpoint":"/v1/responses","completion_window":"24h"}`); isolated.Code != http.StatusNotFound {
		t.Fatalf("cross-key file status=%d body=%s", isolated.Code, isolated.Body.String())
	}
	tests := map[string]string{
		"duplicate custom id": validLine + "\n" + validLine + "\n",
		"wrong method":        `{"custom_id":"one","method":"GET","url":"/v1/responses","body":{"model":"assistant","input":"x"}}` + "\n",
		"wrong url":           `{"custom_id":"one","method":"POST","url":"/v1/chat/completions","body":{"model":"assistant","input":"x"}}` + "\n",
		"mixed model":         validLine + "\n" + `{"custom_id":"two","method":"POST","url":"/v1/responses","body":{"model":"other","input":"x"}}` + "\n",
		"stream":              `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","stream":true}}` + "\n",
		"background":          `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","background":true}}` + "\n",
		"conversation":        `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","conversation":"conv_provider"}}` + "\n",
		"previous response":   `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","previous_response_id":"resp_provider"}}` + "\n",
		"stored":              `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","store":true}}` + "\n",
		"hosted tool":         `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","tools":[{"type":"web_search"}]}}` + "\n",
		"local file":          `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":[{"type":"input_file","file_id":"file_local"}]}}` + "\n",
		"provider file":       `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":[{"type":"input_file","file_id":"provider_document"}]}}` + "\n",
		"provider item":       `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":[{"type":"item_reference","id":"item_provider"}]}}` + "\n",
		"provider container":  `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":[{"type":"container_reference","container_id":"container_provider"}]}}` + "\n",
		"unknown line field":  `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x"},"extra":true}` + "\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			fileID := fileFor(secret, content)
			result := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+fileID+`","endpoint":"/v1/responses","completion_window":"24h"}`)
			if result.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
		})
	}
}

func TestOpenAIBatchRecoveryFinalizesTerminalItems(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batch recovery", Scopes: []string{"batches:manage", "files:manage", "responses:generate"}, ModelPatterns: []string{"assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{3}, 32))
	mux := http.NewServeMux()
	handler.Register(mux)
	uploaded := performFileUpload(t, mux, secret, "batch.jsonl", []byte("{\"custom_id\":\"one\",\"method\":\"POST\",\"url\":\"/v1/responses\",\"body\":{\"model\":\"assistant\",\"input\":\"x\"}}\n"), map[string]string{"purpose": "batch"}, nil)
	var file openAIFile
	_ = json.Unmarshal(uploaded.Body.Bytes(), &file)
	created := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/responses","completion_window":"24h"}`)
	var batch map[string]any
	_ = json.Unmarshal(created.Body.Bytes(), &batch)
	job, ok := handler.claimOpenAIBatch(ctx)
	if !ok {
		var state, status string
		var expiresAt int64
		_ = store.SystemDB().QueryRowContext(ctx, `SELECT i.state,b.status,b.expires_at FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id LIMIT 1`).Scan(&state, &status, &expiresAt)
		t.Fatalf("batch item was not claimable: item=%q batch=%q expires=%d now=%d", state, status, expiresAt, time.Now().UnixMilli())
	}
	line := openAIBatchErrorLine(job.resultID, job.customID, "server_error", "retry finalization")
	ciphertext, nonce, err := handler.sealOpenAIBatchPayload(job.batchID, job.keyID, job.customID, job.ordinal, "result", line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE openai_batch_items SET state='failed',result_bytes=?,result_ciphertext=?,result_nonce=?,finished_at=? WHERE batch_id=? AND ordinal=?`, len(line), ciphertext, nonce, time.Now().UnixMilli(), job.batchID, job.ordinal); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-25 * time.Hour)
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE openai_batches SET created_at=?,in_progress_at=?,expires_at=?,retention_expires_at=? WHERE id=?`, past.UnixMilli(), past.UnixMilli(), past.Add(24*time.Hour).UnixMilli(), past.Add(openAIBatchRetention).UnixMilli(), job.batchID); err != nil {
		t.Fatal(err)
	}
	if err = handler.recoverOpenAIBatches(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := handler.loadOpenAIBatch(ctx, job.keyID, batch["id"].(string))
	if err != nil || row.status != "completed" || row.requestFailed != 1 {
		t.Fatalf("batch=%#v err=%v", row, err)
	}
	uploaded = performFileUpload(t, mux, secret, "expired.jsonl", []byte("{\"custom_id\":\"expired\",\"method\":\"POST\",\"url\":\"/v1/responses\",\"body\":{\"model\":\"assistant\",\"input\":\"x\"}}\n"), map[string]string{"purpose": "batch"}, nil)
	_ = json.Unmarshal(uploaded.Body.Bytes(), &file)
	if result := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/responses","completion_window":"24h"}`); result.Code != http.StatusOK {
		t.Fatalf("create expired recovery batch=%d %s", result.Code, result.Body.String())
	}
	job, ok = handler.claimOpenAIBatch(ctx)
	if !ok {
		t.Fatal("expired batch item was not claimable")
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE openai_batch_items SET state='dispatching',dispatch_started_at=? WHERE batch_id=? AND ordinal=?`, time.Now().UnixMilli(), job.batchID, job.ordinal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `UPDATE openai_batches SET created_at=?,in_progress_at=?,expires_at=?,retention_expires_at=? WHERE id=?`, past.UnixMilli(), past.UnixMilli(), past.Add(24*time.Hour).UnixMilli(), past.Add(openAIBatchRetention).UnixMilli(), job.batchID); err != nil {
		t.Fatal(err)
	}
	if err = handler.recoverOpenAIBatches(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = handler.loadOpenAIBatch(ctx, job.keyID, job.batchID)
	if err != nil || row.status != "expired" || row.usageKnown {
		t.Fatalf("expired recovery batch=%#v err=%v", row, err)
	}
}

func TestOpenAIBatchDoesNotFallbackAndSettlesMalformedSuccessAsFailure(t *testing.T) {
	for name, firstResponse := range map[string]func(http.ResponseWriter){
		"retryable status":  func(response http.ResponseWriter) { http.Error(response, "busy", http.StatusServiceUnavailable) },
		"malformed success": func(response http.ResponseWriter) { _, _ = io.WriteString(response, "not-json") },
	} {
		t.Run(name, func(t *testing.T) {
			var firstCalls, fallbackCalls atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				firstResponse(response)
			}))
			defer first.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				_, _ = io.WriteString(response, `{"id":"resp_fallback","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			}))
			defer fallback.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			firstConnection, model := publishModel(t, ctx, providerService, owner, "openai", first.URL+"/v1", "first", []string{"chat"})
			secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "openai", BaseURL: fallback.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
			if err != nil {
				t.Fatal(err)
			}
			if err = providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
				t.Fatal(err)
			}
			secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "second", []string{"chat"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Weight: 1, Enabled: true}}}); err != nil {
				t.Fatal(err)
			}
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Batch", Scopes: []string{"batches:manage", "files:manage", "responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{9}, 32))
			mux := http.NewServeMux()
			handler.Register(mux)
			input := `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"` + model.ID + `","input":"x","max_output_tokens":8}}` + "\n"
			uploaded := performFileUpload(t, mux, secret, "batch.jsonl", []byte(input), map[string]string{"purpose": "batch"}, nil)
			var file openAIFile
			_ = json.Unmarshal(uploaded.Body.Bytes(), &file)
			created := performOpenAIBatchRequest(t, mux, http.MethodPost, "/api/openai/v1/batches", secret, `{"input_file_id":"`+file.ID+`","endpoint":"/v1/responses","completion_window":"24h"}`)
			if created.Code != http.StatusOK {
				t.Fatalf("create=%d %s", created.Code, created.Body.String())
			}
			job, ok := handler.claimOpenAIBatch(ctx)
			if !ok {
				var state, status string
				var expiresAt int64
				_ = store.SystemDB().QueryRowContext(ctx, `SELECT i.state,b.status,b.expires_at FROM openai_batch_items i JOIN openai_batches b ON b.id=i.batch_id LIMIT 1`).Scan(&state, &status, &expiresAt)
				t.Fatalf("batch item was not claimable: item=%q batch=%q expires=%d now=%d", state, status, expiresAt, time.Now().UnixMilli())
			}
			handler.runOpenAIBatch(ctx, job)
			if firstCalls.Load() != 1 || fallbackCalls.Load() != 0 {
				t.Fatalf("dispatch calls=%d/%d", firstCalls.Load(), fallbackCalls.Load())
			}
			requests, _, err := usageService.ListRequests(ctx, owner, usage.UsageQuery{})
			if err != nil || len(requests) != 1 || len(requests[0].Attempts) != 1 || requests[0].Attempts[0].State != "failed" {
				t.Fatalf("requests=%#v err=%v", requests, err)
			}
			batch, err := handler.loadOpenAIBatch(ctx, job.keyID, job.batchID)
			if err != nil || batch.usageKnown {
				t.Fatalf("batch usage known=%v err=%v", batch.usageKnown, err)
			}
		})
	}
}

func TestOpenAIBatchAggregateUsageIsNullWhenSuccessOmitsUsage(t *testing.T) {
	usage := openAIBatchUsage{known: true}
	line := openAIBatchSuccessLine("batch_req_1234567890123456", "one", http.StatusOK, "req_1", []byte(`{"id":"resp_1","object":"response","status":"completed"}`))
	if usage.add(line, "/v1/responses") {
		t.Fatal("missing usage was accepted")
	}
	value, err := openAIBatchValue(openAIBatchRow{id: "batch_1234567890123456", inputFileID: "file_1234567890123456", endpoint: "/v1/responses", completionWindow: "24h", modelID: "assistant", status: "completed", metadata: []byte("{}"), requestTotal: 1, requestCompleted: 1, createdAt: 1, inProgressAt: 1, expiresAt: 2, terminalAt: sql.NullInt64{Int64: 2, Valid: true}})
	if err != nil || value["usage"] != nil {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	for _, body := range []string{
		`{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":3}}`,
		`{"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":2}}}`,
		`{"usage":{"input_tokens":1,"output_tokens":1,"output_tokens_details":{"reasoning_tokens":-1}}}`,
	} {
		invalid := openAIBatchUsage{known: true}
		if invalid.add(openAIBatchSuccessLine("batch_req_1234567890123456", "one", http.StatusOK, "req_1", []byte(body)), "/v1/responses") {
			t.Fatalf("invalid usage accepted: %s", body)
		}
	}
	overflow := openAIBatchUsage{known: true, input: maxOpenAIBatchUsage}
	if overflow.add(openAIBatchSuccessLine("batch_req_1234567890123456", "one", http.StatusOK, "req_1", []byte(`{"usage":{"input_tokens":1,"output_tokens":0}}`)), "/v1/responses") {
		t.Fatal("overflowing usage accepted")
	}
	totalOverflow := openAIBatchUsage{known: true, input: maxOpenAIBatchUsage - 1}
	if totalOverflow.add(openAIBatchSuccessLine("batch_req_1234567890123456", "one", http.StatusOK, "req_1", []byte(`{"usage":{"input_tokens":0,"output_tokens":2}}`)), "/v1/responses") {
		t.Fatal("overflowing aggregate total accepted")
	}
	chat := openAIBatchUsage{known: true}
	chatLine := openAIBatchSuccessLine("batch_req_1234567890123456", "chat", http.StatusOK, "req_2", []byte(`{"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	if !chat.add(chatLine, "/v1/chat/completions") || chat.input != 4 || chat.output != 2 || chat.cached != 1 || chat.reasoning != 1 {
		t.Fatalf("chat usage=%#v", chat)
	}
	deepSeek := openAIBatchUsage{known: true}
	deepSeekLine := openAIBatchSuccessLine("batch_req_1234567890123456", "deepseek", http.StatusOK, "req_3", []byte(`{"usage":{"prompt_tokens":6,"completion_tokens":2,"total_tokens":8,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":2}}`))
	if !deepSeek.add(deepSeekLine, "/v1/chat/completions") || deepSeek.input != 6 || deepSeek.output != 2 || deepSeek.cached != 4 {
		t.Fatalf("DeepSeek usage=%#v", deepSeek)
	}
	conflict := openAIBatchUsage{known: true}
	conflictLine := openAIBatchSuccessLine("batch_req_1234567890123456", "conflict", http.StatusOK, "req_4", []byte(`{"usage":{"prompt_tokens":6,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3},"prompt_cache_hit_tokens":4}}`))
	if conflict.add(conflictLine, "/v1/chat/completions") {
		t.Fatal("conflicting cache usage was accepted")
	}
	wrongDialect := openAIBatchUsage{known: true}
	if wrongDialect.add(openAIBatchSuccessLine("batch_req_1234567890123456", "wrong", http.StatusOK, "req_5", []byte(`{"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`)), "/v1/chat/completions") {
		t.Fatal("Responses usage fields were accepted for a Chat Batch")
	}
	wrongCacheDetails := openAIBatchUsage{known: true}
	if wrongCacheDetails.add(openAIBatchSuccessLine("batch_req_1234567890123456", "wrong-cache", http.StatusOK, "req_6", []byte(`{"usage":{"prompt_tokens":4,"completion_tokens":2,"input_tokens_details":{"cached_tokens":1}}}`)), "/v1/chat/completions") {
		t.Fatal("Responses cache fields were accepted for a Chat Batch")
	}
	if wrongCacheDetails.add(openAIBatchSuccessLine("batch_req_1234567890123456", "wrong-cache", http.StatusOK, "req_7", []byte(`{"usage":{"input_tokens":4,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}`)), "/v1/responses") {
		t.Fatal("Chat cache fields were accepted for a Responses Batch")
	}
}

func TestParseOpenAIBatchInputBounds(t *testing.T) {
	line := func(customID string) string {
		return `{"custom_id":` + strconv.Quote(customID) + `,"method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x"}}`
	}
	four := strings.Join([]string{line("a"), line("b"), line("c"), line(strings.Repeat("ü", 32))}, "\n") + "\n"
	items, model, err := parseOpenAIBatchInput([]byte(four), "/v1/responses")
	if err != nil || len(items) != 4 || model != "assistant" {
		t.Fatalf("four items=%d model=%q err=%v", len(items), model, err)
	}
	compatibleDisabled := []byte(`{"custom_id":"disabled","method":"POST","url":"/v1/responses","body":{"model":"assistant","input":"x","stream":false,"background":false,"store":false,"conversation":null,"previous_response_id":null}}` + "\n")
	if _, _, err := parseOpenAIBatchInput(compatibleDisabled, "/v1/responses"); err != nil {
		t.Fatalf("disabled fields rejected: %v", err)
	}
	chat := []byte(`{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":{"model":"assistant","messages":[{"role":"user","content":"x"}],"stream":false,"store":false}}` + "\n")
	if items, model, err := parseOpenAIBatchInput(chat, "/v1/chat/completions"); err != nil || len(items) != 1 || model != "assistant" {
		t.Fatalf("chat items=%d model=%q err=%v", len(items), model, err)
	}
	if _, _, err := parseOpenAIBatchInput(chat, "/v1/responses"); err == nil {
		t.Fatal("mismatched Batch endpoint was accepted")
	}
	for _, invalidChat := range [][]byte{
		[]byte(`{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":{"model":"assistant"}}` + "\n"),
		[]byte(`{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":{"model":"assistant","messages":[]}}` + "\n"),
		[]byte(`{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":{"model":"assistant","messages":[{"role":"user","content":"search"}],"web_search_options":{}}}` + "\n"),
	} {
		if _, _, err := parseOpenAIBatchInput(invalidChat, "/v1/chat/completions"); err == nil {
			t.Fatal("Chat Batch without messages was accepted")
		}
	}
	invalid := [][]byte{
		{},
		[]byte(strings.Join([]string{line("a"), line("b"), line("c"), line("d"), line("e")}, "\n")),
		[]byte(line(strings.Repeat("a", 65))),
		append([]byte(`{"custom_id":"`), append([]byte{0xff}, []byte(`","method":"POST","url":"/v1/responses","body":{"model":"assistant"}}`)...)...),
	}
	for index, content := range invalid {
		if _, _, err := parseOpenAIBatchInput(content, "/v1/responses"); err == nil {
			t.Fatalf("invalid case %d accepted", index)
		}
	}
}

func performOpenAIBatchRequest(t *testing.T, handler http.Handler, method, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
