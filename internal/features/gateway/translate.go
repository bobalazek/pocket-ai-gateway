package gateway

import (
	"encoding/json"
	"errors"
	"net/url"
)

type canonicalRequest struct {
	System           string
	Messages         []canonicalMessage
	Tools            []canonicalTool
	MaxTokens        int64
	Stream           bool
	Stops            []string
	OutputSchema     any
	OutputSchemaName string
	StrictOutput     bool
	ToolChoice       string
	ToolName         string
	ParallelTools    *bool
	Temperature      *float64
	TopP             *float64
	TopK             *int64
}
type canonicalMessage struct {
	Role  string
	Parts []canonicalPart
}
type canonicalPart struct {
	Kind, Text, ID, Name         string
	MediaType, Data, URL, Detail string
	Value                        any
}
type canonicalTool struct {
	Name, Description string
	Schema            any
	Strict            bool
}
type canonicalResult struct {
	ID                        string
	Parts                     []canonicalPart
	Stop                      string
	InputTokens, OutputTokens int64
	Refusal                   string
}

func translateRequest(client, target string, body []byte, upstreamModel string) (string, []byte, error) {
	request, err := decodeCanonicalRequest(client, body)
	if err != nil {
		return "", nil, err
	}
	if target == "gemini" && request.ParallelTools != nil && !*request.ParallelTools {
		return "", nil, errors.New("parallel tool disabling cannot be preserved by Gemini translation")
	}
	if (target == "openai" || target == "openai_compatible") && request.TopK != nil {
		return "", nil, errors.New("top_k cannot be preserved by OpenAI translation")
	}
	if target == "gemini" {
		for _, message := range request.Messages {
			for _, part := range message.Parts {
				if part.Kind == "tool_result" && part.Name == "" {
					return "", nil, errors.New("tool result requires a matching named tool call for Gemini translation")
				}
			}
		}
	}
	if target == "gemini" {
		for _, tool := range request.Tools {
			if tool.Strict {
				return "", nil, errors.New("strict function tools cannot be preserved by Gemini translation")
			}
		}
	}
	switch target {
	case "openai", "openai_compatible":
		value := map[string]any{"model": upstreamModel, "messages": encodeOpenAIMessages(request), "stream": request.Stream}
		if request.MaxTokens > 0 {
			value["max_tokens"] = request.MaxTokens
		}
		encodeSampling(value, request, "temperature", "top_p", "")
		if request.Stream {
			value["stream_options"] = map[string]any{"include_usage": true}
		}
		if len(request.Tools) > 0 {
			value["tools"] = encodeOpenAITools(request.Tools)
		}
		encodeOpenAIToolChoice(value, request)
		if len(request.Stops) == 1 {
			value["stop"] = request.Stops[0]
		} else if len(request.Stops) > 1 {
			value["stop"] = request.Stops
		}
		if request.OutputSchema != nil {
			value["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": request.OutputSchemaName, "schema": request.OutputSchema, "strict": request.StrictOutput}}
		}
		return "chat/completions", mustJSON(value), nil
	case "anthropic":
		maxTokens := request.MaxTokens
		if maxTokens == 0 {
			maxTokens = 4096
		}
		value := map[string]any{"model": upstreamModel, "messages": encodeAnthropicMessages(request), "max_tokens": maxTokens, "stream": request.Stream}
		encodeSampling(value, request, "temperature", "top_p", "top_k")
		if request.System != "" {
			value["system"] = request.System
		}
		if len(request.Tools) > 0 {
			value["tools"] = encodeAnthropicTools(request.Tools)
		}
		encodeAnthropicToolChoice(value, request)
		if len(request.Stops) > 0 {
			value["stop_sequences"] = request.Stops
		}
		if request.OutputSchema != nil {
			value["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": request.OutputSchema}}
		}
		return "messages", mustJSON(value), nil
	case "gemini":
		value := map[string]any{"contents": encodeGeminiContents(request)}
		config := map[string]any{}
		encodeSampling(config, request, "temperature", "topP", "topK")
		if request.System != "" {
			value["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": request.System}}}
		}
		if request.MaxTokens > 0 {
			config["maxOutputTokens"] = request.MaxTokens
		}
		if len(request.Stops) > 0 {
			config["stopSequences"] = request.Stops
		}
		if request.OutputSchema != nil {
			config["responseMimeType"] = "application/json"
			config["responseJsonSchema"] = request.OutputSchema
		}
		if len(config) > 0 {
			value["generationConfig"] = config
		}
		if len(request.Tools) > 0 {
			value["tools"] = []any{map[string]any{"functionDeclarations": encodeGeminiTools(request.Tools)}}
		}
		encodeGeminiToolChoice(value, request)
		action := "generateContent"
		if request.Stream {
			action = "streamGenerateContent?alt=sse"
		}
		return "models/" + url.PathEscape(upstreamModel) + ":" + action, mustJSON(value), nil
	}
	return "", nil, errors.New("unsupported target protocol")
}

func translateResponse(client, target, publicModel string, raw []byte) ([]byte, error) {
	result, err := decodeCanonicalResult(target, raw)
	if err != nil {
		return nil, err
	}
	if (client == "openai" || client == "responses") && hasPartKind(result.Parts, "image") {
		return nil, errors.New("upstream image output cannot be represented by this client protocol")
	}
	switch client {
	case "openai":
		message := map[string]any{"role": "assistant", "content": canonicalText(result.Parts)}
		if result.Refusal != "" {
			message["content"], message["refusal"] = nil, result.Refusal
		}
		if calls := openAIToolCalls(result.Parts); len(calls) > 0 {
			message["tool_calls"] = calls
		}
		return json.Marshal(map[string]any{"id": result.ID, "object": "chat.completion", "model": publicModel, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": openAIStop(result.Stop)}}, "usage": map[string]any{"prompt_tokens": result.InputTokens, "completion_tokens": result.OutputTokens, "total_tokens": result.InputTokens + result.OutputTokens}})
	case "anthropic":
		content := anthropicParts(result.Parts)
		if result.Refusal != "" && len(content) == 0 {
			content = []any{map[string]any{"type": "text", "text": result.Refusal}}
		}
		return json.Marshal(map[string]any{"id": result.ID, "type": "message", "role": "assistant", "model": publicModel, "content": content, "stop_reason": anthropicStop(result.Stop), "stop_sequence": nil, "usage": map[string]any{"input_tokens": result.InputTokens, "output_tokens": result.OutputTokens}})
	case "gemini":
		value := map[string]any{"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": geminiParts(result.Parts)}, "finishReason": geminiStop(result.Stop)}}, "usageMetadata": map[string]any{"promptTokenCount": result.InputTokens, "candidatesTokenCount": result.OutputTokens, "totalTokenCount": result.InputTokens + result.OutputTokens}, "modelVersion": publicModel}
		if result.Refusal != "" {
			value["promptFeedback"] = map[string]any{"blockReason": "SAFETY"}
		}
		return json.Marshal(value)
	case "responses":
		output := []any{}
		content := []any{}
		if text := canonicalText(result.Parts); text != "" {
			content = append(content, map[string]any{"type": "output_text", "text": text, "annotations": []any{}})
		}
		if result.Refusal != "" {
			content = append(content, map[string]any{"type": "refusal", "refusal": result.Refusal})
		}
		if len(content) > 0 {
			output = append(output, map[string]any{"id": "msg_translated", "type": "message", "role": "assistant", "status": "completed", "content": content})
		}
		for _, part := range result.Parts {
			if part.Kind == "tool_use" {
				arguments, _ := json.Marshal(part.Value)
				output = append(output, map[string]any{"id": "fc_" + part.ID, "type": "function_call", "call_id": part.ID, "name": part.Name, "arguments": string(arguments), "status": "completed"})
			}
		}
		return json.Marshal(map[string]any{"id": result.ID, "object": "response", "status": "completed", "model": publicModel, "output": output, "usage": map[string]any{"input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "total_tokens": result.InputTokens + result.OutputTokens}})
	}
	return nil, errors.New("unsupported client protocol")
}
