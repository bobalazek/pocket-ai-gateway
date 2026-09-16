package gateway

import (
	"encoding/json"
	"testing"
)

func TestResponseFileSearchValidation(t *testing.T) {
	valid := []struct {
		body            string
		stores, results int
		calls           int64
		include         bool
	}{
		{`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1}`, 1, 10, 1, false},
		{`{"tools":[{"type":"function","name":"local","parameters":{}},{"type":"file_search","vector_store_ids":["vs_1","vs_2"],"max_num_results":50,"filters":{"type":"eq","key":"kind","value":"docs"},"ranking_options":{"score_threshold":0.25}}],"max_tool_calls":4,"max_output_tokens":128,"include":["file_search_call.results"],"stream":false}`, 2, 50, 4, true},
	}
	for _, test := range valid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(test.body), &envelope)
		request, err := validateResponseFileSearch(envelope)
		if err != nil || !request.enabled || len(request.vectorStoreIDs) != test.stores || request.maxResults != test.results || request.maxCalls != test.calls || request.includeResults != test.include {
			t.Fatalf("valid request rejected: %s: %#v, %v", test.body, request, err)
		}
	}
	var filtered map[string]json.RawMessage
	_ = json.Unmarshal([]byte(valid[1].body), &filtered)
	request, _ := validateResponseFileSearch(filtered)
	if request.filter == nil || !request.filter.matches(map[string]any{"kind": "docs"}) || request.scoreThreshold != 0.25 {
		t.Fatalf("file search options not retained: %#v", request)
	}

	invalid := []string{
		`{"tools":[{"type":"file_search","vector_store_ids":[]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1","vs_1"]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1","vs_2","vs_3","vs_4","vs_5","vs_6","vs_7","vs_8","vs_9","vs_10","vs_11"]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"max_num_results":0}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"max_num_results":51}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"filters":{"type":"eq","key":"kind","value":null}}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"ranking_options":{"ranker":"auto"}}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"ranking_options":{"ranker":"none"}}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"ranking_options":{"hybrid_search":null}}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"include":["web_search_call.action.sources"]}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"max_tool_calls":0}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"max_tool_calls":5}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":0}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"stream":true}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"conversation":"conv_1"}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"previous_response_id":"resp_1"}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1,"prompt":{"id":"pmpt_1"}}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]},{"type":"file_search","vector_store_ids":["vs_2"]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"web_search"},{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"function","name":"pocket_ai_gateway_file_search"},{"type":"file_search","vector_store_ids":["vs_1"]}],"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"tool_choice":{"type":"function","name":"pocket_ai_gateway_file_search"},"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"pocket_ai_gateway_file_search"}]},"max_output_tokens":1}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_1"],"unknown":true}],"max_output_tokens":1}`,
	}
	for _, body := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(body), &envelope)
		if _, err := validateResponseFileSearch(envelope); err == nil {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
}

func TestHostedFileSearchDetectionIsTypeSpecific(t *testing.T) {
	if !containsHostedFileSearchTool(json.RawMessage(`[{"type":"file_search"}]`)) {
		t.Fatal("file_search tool was not detected")
	}
	if containsHostedFileSearchTool(json.RawMessage(`[{"type":"function","name":"file_search"}]`)) {
		t.Fatal("function named file_search was treated as a hosted tool")
	}
}
