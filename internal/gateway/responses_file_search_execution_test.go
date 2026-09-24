package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestResponsesFileSearchRunsLocalToolLoopAndAccountsAggregateUsage(t *testing.T) {
	var calls atomic.Int64
	var localFileID string
	var failSecond atomic.Bool
	var omitStoredStatus atomic.Bool
	var parallelExternal atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		if calls.Add(1)%2 == 1 {
			if bytes.Contains(body, []byte(`"type":"file_search"`)) || !bytes.Contains(body, []byte(`"name":"pocket_ai_gateway_file_search"`)) {
				t.Errorf("first upstream request did not contain only the internal function: %s", body)
			}
			if parallelExternal.Load() {
				_, _ = io.WriteString(response, `{"id":"resp_parallel","object":"response","status":"completed","model":"upstream","output":[{"id":"reason_1","type":"reasoning","summary":[]},{"id":"fc_2","type":"function_call","call_id":"call_search_2","status":"completed","name":"pocket_ai_gateway_file_search","arguments":"{\"queries\":[\"gateway facts\"]}"},{"id":"fc_weather","type":"function_call","call_id":"call_weather","status":"completed","name":"weather","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
				return
			}
			_, _ = io.WriteString(response, `{"id":"resp_search","object":"response","status":"completed","model":"upstream","output":[{"id":"fc_1","type":"function_call","call_id":"call_search","status":"completed","name":"pocket_ai_gateway_file_search","arguments":"{\"queries\":[\"gateway facts\"]}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1}}}`)
			return
		}
		if failSecond.Load() {
			response.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(response, `{"error":{"message":"provider busy","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
			return
		}
		if !bytes.Contains(body, []byte(`"type":"function_call_output"`)) || !bytes.Contains(body, []byte(`Pocket AI facts`)) || !bytes.Contains(body, []byte(`"tool_choice":"auto"`)) || !bytes.Contains(body, []byte(`"max_output_tokens":62`)) {
			t.Errorf("follow-up request did not contain the local search result: %s", body)
		}
		if localFileID != "" && (bytes.Contains(body, []byte(localFileID)) || bytes.Contains(body, []byte(`local-only`))) {
			t.Errorf("follow-up request leaked local file metadata: %s", body)
		}
		status := `,"status":"completed"`
		if omitStoredStatus.Load() {
			status = ""
		}
		_, _ = io.WriteString(response, `{"id":"resp_final","object":"response"`+status+`,"model":"upstream","tools":[{"type":"function","name":"pocket_ai_gateway_file_search"}],"tool_choice":{"type":"function","name":"pocket_ai_gateway_file_search"},"max_output_tokens":62,"max_tool_calls":999,"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Found it","annotations":[]}]}],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10,"input_tokens_details":{"cached_tokens":2}}}`)
	}))
	defer upstream.Close()

	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "upstream", []string{"chat"})
	key, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "File search", Scopes: []string{"responses:generate", responseFileSearchScope, "files:manage", "vector_stores:manage"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	cacheRate := "1"
	if _, err = usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: model.ID, InputUSDPerMillion: "1", OutputUSDPerMillion: "2", CacheReadUSDPerMillion: &cacheRate, Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "key", ScopeID: key.ID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "100"}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, make([]byte, 32))
	mux := http.NewServeMux()
	handler.Register(mux)

	upload := performFileUpload(t, mux, secret, "facts.pdf", vectorStoreTestPDFFacts(), map[string]string{"purpose": "user_data"}, nil)
	var file openAIFile
	if upload.Code != http.StatusOK || json.Unmarshal(upload.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", upload.Code, upload.Body.String())
	}
	localFileID = file.ID
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Knowledge"}`)
	var vectorStore vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &vectorStore) != nil {
		t.Fatalf("create store status=%d body=%s", created.Code, created.Body.String())
	}
	attached := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+vectorStore.ID+"/files", secret, `{"file_id":"`+file.ID+`","attributes":{"secret":"local-only"}}`)
	if attached.Code != http.StatusOK {
		t.Fatalf("attach status=%d body=%s", attached.Code, attached.Body.String())
	}

	body := `{"model":"assistant","store":false,"input":"Use the knowledge base","max_output_tokens":64,"max_tool_calls":2,"tool_choice":{"type":"file_search"},"include":["file_search_call.results"],"tools":[{"type":"file_search","vector_store_ids":["` + vectorStore.ID + `"],"max_num_results":3}]}`
	result := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
	if result.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d body=%s", result.Code, calls.Load(), result.Body.String())
	}
	var decoded struct {
		Model           string            `json:"model"`
		Tools           []json.RawMessage `json:"tools"`
		ToolChoice      json.RawMessage   `json:"tool_choice"`
		MaxOutputTokens int64             `json:"max_output_tokens"`
		MaxToolCalls    int64             `json:"max_tool_calls"`
		Output          []struct {
			Type    string                         `json:"type"`
			Queries []string                       `json:"queries"`
			Results []responseFileSearchResultItem `json:"results"`
		} `json:"output"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
			Details      struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(result.Body.Bytes(), &decoded) != nil || decoded.Model != "assistant" || len(decoded.Tools) != 1 || !bytes.Contains(decoded.Tools[0], []byte(`"type":"file_search"`)) || !bytes.Equal(decoded.ToolChoice, []byte(`{"type":"file_search"}`)) || decoded.MaxOutputTokens != 64 || decoded.MaxToolCalls != 2 || len(decoded.Output) != 2 || decoded.Output[0].Type != "file_search_call" || strings.Join(decoded.Output[0].Queries, " ") != "gateway facts" || len(decoded.Output[0].Results) != 1 || decoded.Output[0].Results[0].FileID != file.ID || decoded.Output[1].Type != "message" || bytes.Contains(result.Body.Bytes(), []byte(responseFileSearchFunctionName)) {
		t.Fatalf("unexpected response: %s", result.Body.String())
	}
	if decoded.Usage.InputTokens != 10 || decoded.Usage.OutputTokens != 5 || decoded.Usage.TotalTokens != 15 || decoded.Usage.Details.CachedTokens != 3 {
		t.Fatalf("unexpected aggregate usage: %#v", decoded.Usage)
	}
	var requestTools, responseTools, inputTokens, outputTokens, estimated int64
	var cost sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT attempts.request_tool_count,attempts.response_tool_call_count,attempts.input_tokens,attempts.output_tokens,attempts.as_recorded_cost_nanos,attempts.estimated_tokens FROM attempts JOIN requests ON requests.id=attempts.request_id WHERE requests.key_id=?`, key.ID).Scan(&requestTools, &responseTools, &inputTokens, &outputTokens, &cost, &estimated); err != nil {
		t.Fatal(err)
	}
	if requestTools != 1 || responseTools != 1 || inputTokens != 10 || outputTokens != 5 || !cost.Valid || cost.Int64 != 20_000 || estimated < maxVectorStoreContentChunkBytes {
		t.Fatalf("accounting request=%d response=%d input=%d output=%d cost=%v estimate=%d", requestTools, responseTools, inputTokens, outputTokens, cost, estimated)
	}

	failSecond.Store(true)
	failed := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
	if failed.Code != http.StatusTooManyRequests || calls.Load() != 4 {
		t.Fatalf("failed status=%d calls=%d body=%s", failed.Code, calls.Load(), failed.Body.String())
	}
	var state, usageStatus, toolStatus string
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,response_tool_call_count,tool_call_status FROM attempts WHERE request_id=?`, failed.Header().Get(pocketAIRequestIDHeader)).Scan(&state, &usageStatus, &inputTokens, &outputTokens, &cost, &responseTools, &toolStatus); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || usageStatus != "provider_reported" || inputTokens != 3 || outputTokens != 2 || !cost.Valid || cost.Int64 != 7_000 || responseTools != 1 || toolStatus != "incomplete" {
		t.Fatalf("failed accounting state=%s usage=%s input=%d output=%d cost=%v tools=%d/%s", state, usageStatus, inputTokens, outputTokens, cost, responseTools, toolStatus)
	}

	failSecond.Store(false)
	omitStoredStatus.Store(true)
	stored := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", strings.Replace(body, `"store":false,`, "", 1))
	if stored.Code != http.StatusBadGateway || calls.Load() != 6 {
		t.Fatalf("stored status=%d calls=%d body=%s", stored.Code, calls.Load(), stored.Body.String())
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,response_tool_call_count,tool_call_status FROM attempts WHERE request_id=?`, stored.Header().Get(pocketAIRequestIDHeader)).Scan(&state, &usageStatus, &inputTokens, &outputTokens, &cost, &responseTools, &toolStatus); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || usageStatus != "provider_reported" || inputTokens != 10 || outputTokens != 5 || !cost.Valid || cost.Int64 != 20_000 || responseTools != 1 || toolStatus != "incomplete" {
		t.Fatalf("stored accounting state=%s usage=%s input=%d output=%d cost=%v tools=%d/%s", state, usageStatus, inputTokens, outputTokens, cost, responseTools, toolStatus)
	}

	omitStoredStatus.Store(false)
	parallelExternal.Store(true)
	parallel := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", body)
	if parallel.Code != http.StatusOK || calls.Load() != 7 || bytes.Contains(parallel.Body.Bytes(), []byte(responseFileSearchFunctionName)) {
		t.Fatalf("parallel status=%d calls=%d body=%s", parallel.Code, calls.Load(), parallel.Body.String())
	}
	var parallelBody struct {
		Output []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"output"`
	}
	if json.Unmarshal(parallel.Body.Bytes(), &parallelBody) != nil || len(parallelBody.Output) != 3 || parallelBody.Output[0].Type != "reasoning" || parallelBody.Output[1].Type != "file_search_call" || parallelBody.Output[2].Name != "weather" {
		t.Fatalf("parallel output order changed: %s", parallel.Body.String())
	}
}

func TestResponseFileSearchReservationBoundsAllRounds(t *testing.T) {
	input, err := responseFileSearchInputReservation(100, 20, responseFileSearchRequest{maxCalls: 2, maxResults: 3})
	wantInput := int64(3*100 + 3*(3*maxVectorStoreContentChunkBytes+20))
	if err != nil || input != wantInput {
		t.Fatalf("input=%d error=%v want input=%d", input, err, wantInput)
	}
}

func TestResponseFileSearchAccountingFailureIsCompact(t *testing.T) {
	raw, err, ok := responseFileSearchAccountingFailure(errors.New("local reconstruction failed"), map[string]any{
		"input_tokens":  7,
		"output_tokens": 3,
		"total_tokens":  10,
	}, true, 2)
	if !ok || err == nil || len(raw) > 1024 {
		t.Fatalf("ok=%t error=%v bytes=%d body=%s", ok, err, len(raw), raw)
	}
	usage := parseUsageDetails("responses", raw)
	calls, status := parseToolMetadata("responses", raw)
	if usage.inputTokens == nil || *usage.inputTokens != 7 || usage.outputTokens == nil || *usage.outputTokens != 3 || calls != 2 || status != "incomplete" {
		t.Fatalf("usage=%#v calls=%d status=%s body=%s", usage, calls, status, raw)
	}
}

func TestResponseFileSearchPreservesOutputOrderAndAllowedTools(t *testing.T) {
	body := []byte(`{"model":"assistant","input":"search","max_output_tokens":20,"tools":[{"type":"file_search","vector_store_ids":["vs_1"]},{"type":"function","name":"weather","parameters":{}}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"file_search"}]}}`)
	working, publicFields, err := prepareResponseFileSearchRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	output := []json.RawMessage{
		json.RawMessage(`{"id":"reason_1","type":"reasoning","summary":[]}`),
		json.RawMessage(`{"id":"fc_search","type":"function_call","call_id":"call_search","name":"pocket_ai_gateway_file_search","arguments":"{\"queries\":[\"docs\"]}"}`),
		json.RawMessage(`{"id":"fc_weather","type":"function_call","call_id":"call_weather","name":"weather","arguments":"{}"}`),
	}
	internal, external, err := responseFileSearchFunctionCalls(output)
	if err != nil || len(internal) != 1 || !external {
		t.Fatalf("calls=%#v external=%t error=%v", internal, external, err)
	}
	publicCall, _ := json.Marshal(responseFileSearchCall{ID: "fs_public", Type: "file_search_call", Status: "completed", Queries: []string{"docs"}})
	visible := appendResponseFileSearchOutput(nil, output, internal, map[int]json.RawMessage{internal[0].index: publicCall})
	visibleJSON, _ := json.Marshal(visible)
	if len(visible) != 3 || !bytes.Contains(visible[0], []byte(`"type":"reasoning"`)) || !bytes.Contains(visible[1], []byte(`"type":"file_search_call"`)) || !bytes.Contains(visible[2], []byte(`"name":"weather"`)) || bytes.Contains(visibleJSON, []byte(responseFileSearchFunctionName)) {
		t.Fatalf("output order or privacy changed: %s", visibleJSON)
	}
	continued, err := appendResponseFileSearchInput(working, output[:2], []json.RawMessage{json.RawMessage(`{"type":"function_call_output","call_id":"call_search","output":"{}"}`)}, 18)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	var choice struct {
		Type  string                   `json:"type"`
		Mode  string                   `json:"mode"`
		Tools []map[string]interface{} `json:"tools"`
	}
	_ = json.Unmarshal(continued, &envelope)
	if json.Unmarshal(envelope["tool_choice"], &choice) != nil || choice.Type != "allowed_tools" || choice.Mode != "auto" || len(choice.Tools) != 1 || choice.Tools[0]["name"] != responseFileSearchFunctionName {
		t.Fatalf("allowed tool choice was not preserved: %s", envelope["tool_choice"])
	}
	if string(publicFields["tool_choice"]) != `{"type":"allowed_tools","mode":"required","tools":[{"type":"file_search"}]}` {
		t.Fatalf("public tool choice changed: %s", publicFields["tool_choice"])
	}

	_, defaults, err := prepareResponseFileSearchRequest([]byte(`{"model":"assistant","input":"search","max_output_tokens":20,"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}]}`))
	if err != nil || string(defaults["tool_choice"]) != `"auto"` {
		t.Fatalf("default public tool choice=%s error=%v", defaults["tool_choice"], err)
	}
}
