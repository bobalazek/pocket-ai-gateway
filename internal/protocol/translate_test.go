package protocol

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrossProtocolRequestMappingsPreserveTextAndTools(t *testing.T) {
	tests := []struct{ client, target, body, path, contains string }{
		{"openai", "anthropic", `{"model":"assistant","max_tokens":12,"messages":[{"role":"system","content":"Rules"},{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`, "messages", `"input_schema":{"type":"object"}`},
		{"anthropic", "gemini", `{"model":"assistant","max_tokens":12,"system":"Rules","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Ljubljana"}}]}]}`, "models/upstream:generateContent", `"functionCall":{"args":{"city":"Ljubljana"},"id":"call_1","name":"weather"}`},
		{"gemini", "openai", `{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"weather","response":{"temp":20}}}]}]}`, "chat/completions", `"tool_call_id":"call_1"`},
		{"responses", "anthropic", `{"model":"assistant","input":"Hi","max_output_tokens":12,"store":false}`, "messages", `"text":"Hi"`},
	}
	for _, test := range tests {
		path, body, err := TranslateRequest(test.client, test.target, []byte(test.body), "upstream")
		if err != nil || !strings.HasPrefix(path, test.path) || !strings.Contains(string(body), test.contains) {
			t.Errorf("%s->%s path=%s body=%s err=%v", test.client, test.target, path, body, err)
		}
	}
}

func TestCrossProtocolRequestMappingsPreserveLimitsAndStops(t *testing.T) {
	_, body, err := TranslateRequest("openai", "anthropic", []byte(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":12,"stop":["END","DONE"]}`), "claude")
	if err != nil || !strings.Contains(string(body), `"max_tokens":12`) || !strings.Contains(string(body), `"stop_sequences":["END","DONE"]`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
	_, body, err = TranslateRequest("gemini", "anthropic", []byte(`{"contents":[{"parts":[{"text":"Hi"}]}]}`), "claude")
	if err != nil || !strings.Contains(string(body), `"max_tokens":4096`) {
		t.Fatalf("default max body=%s err=%v", body, err)
	}
	if _, _, err := TranslateRequest("responses", "gemini", []byte(`{"input":"Hi","store":false,"previous_response_id":"resp_1"}`), "gemini"); err == nil || !strings.Contains(err.Error(), "previous_response_id") {
		t.Fatalf("unsupported stateful response err=%v", err)
	}
	if _, _, err := TranslateRequest("openai", "gemini", []byte(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":1.5}`), "gemini"); err == nil {
		t.Fatal("fractional token limit was accepted")
	}
}

func TestCrossProtocolRequestMappingsPreserveSampling(t *testing.T) {
	tests := []struct{ client, target, body, want string }{
		{"openai", "anthropic", `{"messages":[{"role":"user","content":"Hi"}],"n":1,"temperature":0.4,"top_p":0.8}`, `"temperature":0.4`},
		{"anthropic", "gemini", `{"messages":[{"role":"user","content":"Hi"}],"temperature":0.4,"top_p":0.8,"top_k":20}`, `"topK":20`},
		{"gemini", "anthropic", `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"candidateCount":1,"temperature":0.4,"topP":0.8,"topK":20}}`, `"top_k":20`},
	}
	for _, test := range tests {
		_, body, err := TranslateRequest(test.client, test.target, []byte(test.body), "upstream")
		if err != nil || !strings.Contains(string(body), test.want) {
			t.Errorf("%s->%s body=%s err=%v", test.client, test.target, body, err)
		}
	}
	for _, test := range []struct{ client, target, body string }{
		{"openai", "anthropic", `{"messages":[{"role":"user","content":"Hi"}],"n":2}`},
		{"anthropic", "openai", `{"messages":[{"role":"user","content":"Hi"}],"top_k":20}`},
		{"gemini", "anthropic", `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"candidateCount":2}}`},
	} {
		if _, _, err := TranslateRequest(test.client, test.target, []byte(test.body), "upstream"); err == nil {
			t.Errorf("%s->%s silently dropped an unsupported sampling control", test.client, test.target)
		}
	}
}

func TestCrossProtocolImagesStructuredOutputAndAffinity(t *testing.T) {
	tests := []struct{ client, target, body, want string }{
		{"openai", "anthropic", `{"messages":[{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aA=="}}]}]}`, `"source":{"data":"aA==","media_type":"image/png","type":"base64"}`},
		{"gemini", "openai", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aA=="}}]}]}`, `"url":"data:image/png;base64,aA=="`},
		{"openai", "gemini", `{"messages":[{"role":"user","content":"JSON"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object"}}}}`, `"responseJsonSchema":{"type":"object"}`},
		{"anthropic", "openai", `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"weather","disable_parallel_tool_use":true}}`, `"parallel_tool_calls":false`},
	}
	for _, test := range tests {
		_, body, err := TranslateRequest(test.client, test.target, []byte(test.body), "upstream")
		if err != nil || !strings.Contains(string(body), test.want) {
			t.Errorf("%s->%s body=%s err=%v", test.client, test.target, body, err)
		}
	}
	for _, test := range []struct{ protocol, body, want string }{
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret"}]}]}`, "provider affinity"},
		{"gemini", `{"contents":[{"parts":[{"text":"private","thoughtSignature":"opaque"}]}]}`, "provider affinity"},
		{"openai", `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x"}}]}]}`, "not supported"},
	} {
		if _, _, err := TranslateRequest(test.protocol, "openai", []byte(test.body), "upstream"); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s affinity err=%v", test.protocol, err)
		}
	}
}

func TestTranslatedStreamPreservesFragmentedToolArguments(t *testing.T) {
	source := strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"weather\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Ljubljana\\\"}\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	recorder := httptest.NewRecorder()
	status, raw, err := TranslateStream(recorder, source, "openai", "anthropic", "assistant")
	body, _ := io.ReadAll(recorder.Result().Body)
	if err != nil || status != 200 || len(raw) == 0 || !strings.Contains(string(body), `"arguments":"{\"city\":"`) || !strings.Contains(string(body), `"finish_reason":"tool_calls"`) || !strings.Contains(string(body), `"prompt_tokens":7`) {
		t.Fatalf("status=%d body=%s err=%v", status, body, err)
	}
}

func TestTranslatedStreamRejectsMalformedOrTruncatedOutput(t *testing.T) {
	for _, source := range []string{"data: not-json\n\n", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"} {
		recorder := httptest.NewRecorder()
		_, _, err := TranslateStream(recorder, strings.NewReader(source), "anthropic", "openai", "assistant")
		if err == nil {
			t.Fatalf("source %q was accepted", source)
		}
	}
}

func TestCrossProtocolResponseMappingsPreserveRefusals(t *testing.T) {
	tests := []struct{ client, target, body, want string }{
		{"anthropic", "openai", `{"id":"chat_1","choices":[{"message":{"content":null,"refusal":"I cannot help with that"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, `"stop_reason":"refusal"`},
		{"openai", "gemini", `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":0}}`, `"refusal":"Request blocked by provider safety policy: SAFETY"`},
		{"gemini", "anthropic", `{"id":"msg_1","content":[],"stop_reason":"refusal","usage":{"input_tokens":2,"output_tokens":0}}`, `"finishReason":"SAFETY"`},
	}
	for _, test := range tests {
		body, err := TranslateResponse(test.client, test.target, "assistant", []byte(test.body))
		if err != nil || !strings.Contains(string(body), test.want) {
			t.Errorf("%s<-%s body=%s err=%v", test.client, test.target, body, err)
		}
	}
}

func TestTranslatedStreamClientShapes(t *testing.T) {
	sources := map[string]string{
		"openai":    "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n",
		"anthropic": "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"gemini":    "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n",
	}
	tests := []struct{ client, target, want string }{
		{"gemini", "openai", `"candidates"`},
		{"openai", "anthropic", `"object":"chat.completion.chunk"`}, {"gemini", "anthropic", `"candidates"`},
		{"openai", "gemini", `"object":"chat.completion.chunk"`},
		{"responses", "openai", "event: response.completed"}, {"responses", "anthropic", "event: response.completed"}, {"responses", "gemini", "event: response.completed"},
	}
	for _, test := range tests {
		t.Run(test.client+"-from-"+test.target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			_, _, err := TranslateStream(recorder, strings.NewReader(sources[test.target]), test.client, test.target, "assistant")
			body, _ := io.ReadAll(recorder.Result().Body)
			if err != nil || !strings.Contains(string(body), test.want) || !strings.Contains(string(body), "Hello") {
				t.Fatalf("body=%s err=%v", body, err)
			}
		})
	}
	if _, _, err := TranslateStream(httptest.NewRecorder(), strings.NewReader(sources["openai"]), "anthropic", "openai", "assistant"); err == nil {
		t.Fatal("cross-provider Anthropic stream was accepted without first-event input usage")
	}
}

func TestCrossProtocolResponseMappingsPreserveUsageAndTools(t *testing.T) {
	tests := []struct{ client, target, body, contains string }{
		{"openai", "anthropic", `{"id":"msg_1","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Ljubljana"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":2}}`, `"finish_reason":"tool_calls"`},
		{"anthropic", "gemini", `{"responseId":"g_1","candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`, `"input_tokens":5`},
		{"gemini", "openai", `{"id":"chat_1","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, `"totalTokenCount":7`},
		{"responses", "anthropic", `{"id":"msg_1","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`, `"object":"response"`},
	}
	for _, test := range tests {
		body, err := TranslateResponse(test.client, test.target, "assistant", []byte(test.body))
		if err != nil || !strings.Contains(string(body), test.contains) {
			t.Errorf("%s<-%s body=%s err=%v", test.client, test.target, body, err)
		}
	}
}

func TestResponsesTranslationKeepsOneMessageAndUniqueToolItems(t *testing.T) {
	body, err := TranslateResponse("responses", "openai", "assistant", []byte(`{"id":"chat_1","choices":[{"message":{"content":"Partial","refusal":"Cannot continue","tool_calls":[{"id":"call_1","function":{"name":"one","arguments":"{}"}},{"id":"call_2","function":{"name":"two","arguments":"{}"}}]},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	if err != nil || strings.Count(string(body), `"type":"message"`) != 1 || !strings.Contains(string(body), `"type":"output_text"`) || !strings.Contains(string(body), `"type":"refusal"`) || !strings.Contains(string(body), `"id":"fc_call_1"`) || !strings.Contains(string(body), `"id":"fc_call_2"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestResponsesStreamCombinesTextAndRefusal(t *testing.T) {
	source := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"Partial\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"refusal\":\"Cannot continue\"},\"finish_reason\":\"content_filter\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	recorder := httptest.NewRecorder()
	_, _, err := TranslateStream(recorder, source, "responses", "openai", "assistant")
	body, _ := io.ReadAll(recorder.Result().Body)
	if err != nil || strings.Count(string(body), `"type":"response.output_item.done"`) != 1 || !strings.Contains(string(body), `"type":"output_text"`) || !strings.Contains(string(body), `"type":"refusal"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}
