package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/protocol"
)

func TestParseUsageDetailsNormalizesCacheReads(t *testing.T) {
	tests := []struct {
		name, dialect, raw   string
		input, output, cache int64
	}{
		{"OpenAI chat", "openai", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`, 10, 2, 4},
		{"OpenAI Responses", "responses", `{"usage":{"input_tokens":8,"output_tokens":3,"input_tokens_details":{"cached_tokens":5}}}`, 8, 3, 5},
		{"DeepSeek hit and miss", "openai", `{"usage":{"prompt_tokens":12,"completion_tokens":2,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":5}}`, 12, 2, 7},
		{"DeepSeek miss derives hit", "openai", `{"usage":{"prompt_tokens":12,"completion_tokens":2,"prompt_cache_miss_tokens":5}}`, 12, 2, 7},
		{"Gemini", "gemini", `{"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"cachedContentTokenCount":5}}`, 9, 2, 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed := parseUsageDetails(test.dialect, []byte(test.raw))
			if parsed.inputTokens == nil || *parsed.inputTokens != test.input || parsed.outputTokens == nil || *parsed.outputTokens != test.output || parsed.cacheReadInputTokens == nil || *parsed.cacheReadInputTokens != test.cache {
				t.Fatalf("parsed=%#v", parsed)
			}
		})
	}
}

func TestParseUsageDetailsRejectsInvalidCacheReads(t *testing.T) {
	for _, test := range []struct{ dialect, raw string }{
		{"openai", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":null}}}`},
		{"openai", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"input_tokens_details":{"cached_tokens":11}}}`},
		{"openai", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":5}}`},
		{"gemini", `{"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"cachedContentTokenCount":null}}`},
	} {
		parsed := parseUsageDetails(test.dialect, []byte(test.raw))
		if parsed.inputTokens != nil || parsed.outputTokens != nil || parsed.cacheReadInputTokens != nil {
			t.Fatalf("dialect=%s parsed=%#v", test.dialect, parsed)
		}
	}
}

func TestTranslatedStreamsPreserveProviderCacheUsageForAccounting(t *testing.T) {
	tests := []struct {
		name, target, source          string
		input, output, read, creation int64
	}{
		{"OpenAI", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata: [DONE]\n\n", 10, 2, 4, 0},
		{"DeepSeek", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2,\"prompt_cache_hit_tokens\":7,\"prompt_cache_miss_tokens\":5}}\n\ndata: [DONE]\n\n", 12, 2, 7, 0},
		{"Gemini", "gemini", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":2,\"cachedContentTokenCount\":5}}\n\n", 9, 2, 5, 0},
		{"Anthropic", "anthropic", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0,\"cache_creation_input_tokens\":3,\"cache_read_input_tokens\":4}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", 9, 1, 4, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, metadata, err := protocol.TranslateStream(httptest.NewRecorder(), strings.NewReader(test.source), "responses", test.target, "assistant")
			if err != nil {
				t.Fatal(err)
			}
			parsed := parseUsageDetails(test.target, metadata)
			if parsed.inputTokens == nil || *parsed.inputTokens != test.input || parsed.outputTokens == nil || *parsed.outputTokens != test.output || parsed.cacheReadInputTokens == nil || *parsed.cacheReadInputTokens != test.read {
				t.Fatalf("metadata=%s parsed=%#v", metadata, parsed)
			}
			if test.creation == 0 && parsed.cacheCreationInputTokens != nil || test.creation != 0 && (parsed.cacheCreationInputTokens == nil || *parsed.cacheCreationInputTokens != test.creation) {
				t.Fatalf("metadata=%s creation=%v", metadata, parsed.cacheCreationInputTokens)
			}
		})
	}
}

func TestTranslatedStreamsRejectMalformedLaterUsage(t *testing.T) {
	tests := []struct{ name, target, source string }{
		{"OpenAI", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":null,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"},
		{"DeepSeek", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1}}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":\"bad\",\"completion_tokens\":2,\"prompt_cache_hit_tokens\":7,\"prompt_cache_miss_tokens\":5}}\n\ndata: [DONE]\n\n"},
		{"Gemini", "gemini", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":1}}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":-1,\"candidatesTokenCount\":2}}\n\n"},
		{"Anthropic", "anthropic", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":null}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, metadata, err := protocol.TranslateStream(httptest.NewRecorder(), strings.NewReader(test.source), "responses", test.target, "assistant")
			parsed := parseUsageDetails(test.target, metadata)
			if err == nil || parsed.inputTokens != nil || parsed.outputTokens != nil {
				t.Fatalf("err=%v metadata=%s parsed=%#v", err, metadata, parsed)
			}
		})
	}
}

func TestTranslatedStreamsRejectMissingTerminalUsage(t *testing.T) {
	tests := []struct{ name, target, source string }{
		{"OpenAI", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"first\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" second\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"DeepSeek", "openai", "data: {\"choices\":[{\"delta\":{\"content\":\"first\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1,\"prompt_cache_hit_tokens\":7,\"prompt_cache_miss_tokens\":5}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" second\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"Gemini", "gemini", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"first\"}]}}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":1,\"cachedContentTokenCount\":5}}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" second\"}]},\"finishReason\":\"STOP\"}]}\n\n"},
		{"Anthropic", "anthropic", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":1}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, metadata, err := protocol.TranslateStream(httptest.NewRecorder(), strings.NewReader(test.source), "responses", test.target, "assistant")
			parsed := parseUsageDetails(test.target, metadata)
			if err == nil || parsed.inputTokens != nil || parsed.outputTokens != nil {
				t.Fatalf("err=%v metadata=%s parsed=%#v", err, metadata, parsed)
			}
		})
	}
}
