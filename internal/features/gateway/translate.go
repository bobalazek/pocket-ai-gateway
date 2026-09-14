package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
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

func decodeCanonicalRequest(protocol string, raw []byte) (canonicalRequest, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return canonicalRequest{}, err
	}
	var result canonicalRequest
	var err error
	result.Stream, _ = object["stream"].(bool)
	switch protocol {
	case "openai":
		if err := rejectPresent(object, "reasoning_effort", "modalities", "audio", "prediction", "web_search_options", "frequency_penalty", "presence_penalty", "logit_bias", "logprobs", "top_logprobs", "seed", "service_tier", "user"); err != nil {
			return result, err
		}
		if err := requireSingleCandidate(object, "n"); err != nil {
			return result, err
		}
		result.Temperature, err = optionalNumber(object, "temperature")
		if err != nil {
			return result, err
		}
		result.TopP, err = optionalNumber(object, "top_p")
		if err != nil {
			return result, err
		}
		result.MaxTokens, err = optionalInteger(object, "max_tokens")
		if err != nil {
			return result, err
		}
		if result.MaxTokens == 0 {
			result.MaxTokens, err = optionalInteger(object, "max_completion_tokens")
			if err != nil {
				return result, err
			}
		}
		result.Stops, err = stopValues(object["stop"])
		if err != nil {
			return result, err
		}
		result.OutputSchema, result.OutputSchemaName, result.StrictOutput, err = openAIOutputSchema(object["response_format"])
		if err != nil {
			return result, err
		}
		result.ToolChoice, result.ToolName, result.ParallelTools, err = openAIToolChoice(object)
		if err != nil {
			return result, err
		}
		for _, item := range array(object["messages"]) {
			message := objectMap(item)
			role, _ := message["role"].(string)
			parts, err := decodeOpenAIContent(message["content"])
			if err != nil {
				return result, err
			}
			if role == "system" || role == "developer" {
				if hasNonText(parts) {
					return result, errors.New("system and developer messages must contain text when translated")
				}
				result.System = appendSystem(result.System, canonicalText(parts))
				continue
			}
			current := canonicalMessage{Role: role}
			if role == "tool" {
				if hasNonText(parts) {
					return result, errors.New("tool results must contain text when translated")
				}
				current.Parts = append(current.Parts, canonicalPart{Kind: "tool_result", ID: stringValue(message["tool_call_id"]), Text: canonicalText(parts)})
			} else {
				current.Parts = append(current.Parts, parts...)
			}
			for _, call := range array(message["tool_calls"]) {
				c := objectMap(call)
				function := objectMap(c["function"])
				var args any
				if value := stringValue(function["arguments"]); value != "" {
					if json.Unmarshal([]byte(value), &args) != nil {
						return result, errors.New("tool arguments must be valid JSON")
					}
				}
				current.Parts = append(current.Parts, canonicalPart{Kind: "tool_use", ID: stringValue(c["id"]), Name: stringValue(function["name"]), Value: args})
			}
			result.Messages = append(result.Messages, current)
		}
		for _, item := range array(object["tools"]) {
			toolObject := objectMap(item)
			if stringValue(toolObject["type"]) != "function" {
				return result, errors.New("only function tools can be translated")
			}
			function := objectMap(toolObject["function"])
			strict, _ := function["strict"].(bool)
			result.Tools = append(result.Tools, canonicalTool{Name: stringValue(function["name"]), Description: stringValue(function["description"]), Schema: function["parameters"], Strict: strict})
		}
	case "anthropic":
		if err := rejectPresent(object, "thinking", "mcp_servers", "container", "metadata", "service_tier"); err != nil {
			return result, err
		}
		result.Temperature, err = optionalNumber(object, "temperature")
		if err != nil {
			return result, err
		}
		result.TopP, err = optionalNumber(object, "top_p")
		if err != nil {
			return result, err
		}
		result.TopK, err = optionalPositiveInteger(object, "top_k")
		if err != nil {
			return result, err
		}
		if err := validateAnthropicParts(object["system"]); err != nil {
			return result, errors.New("Anthropic system content must be translatable text")
		}
		systemParts := decodeAnthropicParts(object["system"])
		if hasNonText(systemParts) {
			return result, errors.New("Anthropic system content must contain text")
		}
		result.System = canonicalText(systemParts)
		result.MaxTokens, err = optionalInteger(object, "max_tokens")
		if err != nil {
			return result, err
		}
		result.Stops, err = stopValues(object["stop_sequences"])
		if err != nil {
			return result, err
		}
		format := objectMap(objectMap(object["output_config"])["format"])
		if len(format) > 0 {
			if stringValue(format["type"]) != "json_schema" || format["schema"] == nil {
				return result, errors.New("unsupported Anthropic output format")
			}
			result.OutputSchema, result.OutputSchemaName, result.StrictOutput = format["schema"], "response", true
		}
		result.ToolChoice, result.ToolName, result.ParallelTools, err = anthropicToolChoice(object["tool_choice"])
		if err != nil {
			return result, err
		}
		for _, item := range array(object["messages"]) {
			message := objectMap(item)
			current := canonicalMessage{Role: stringValue(message["role"])}
			if err := validateAnthropicParts(message["content"]); err != nil {
				return result, err
			}
			current.Parts = decodeAnthropicParts(message["content"])
			result.Messages = append(result.Messages, current)
		}
		for _, item := range array(object["tools"]) {
			tool := objectMap(item)
			strict, _ := tool["strict"].(bool)
			result.Tools = append(result.Tools, canonicalTool{Name: stringValue(tool["name"]), Description: stringValue(tool["description"]), Schema: tool["input_schema"], Strict: strict})
		}
	case "responses":
		if stored, ok := object["store"].(bool); !ok || stored {
			return result, errors.New("store:false is required")
		}
		for _, field := range []string{"background", "conversation", "previous_response_id"} {
			if value, exists := object[field]; exists && value != nil && value != "" && value != false {
				return result, fmt.Errorf("%s is not supported for translated Responses requests", field)
			}
		}
		if err := rejectPresent(object, "reasoning", "include", "prompt"); err != nil {
			return result, err
		}
		result.Temperature, err = optionalNumber(object, "temperature")
		if err != nil {
			return result, err
		}
		result.TopP, err = optionalNumber(object, "top_p")
		if err != nil {
			return result, err
		}
		result.MaxTokens, err = optionalInteger(object, "max_output_tokens")
		if err != nil {
			return result, err
		}
		result.System = stringValue(object["instructions"])
		format := objectMap(objectMap(object["text"])["format"])
		if len(format) > 0 {
			result.OutputSchema, result.OutputSchemaName, result.StrictOutput, err = openAIOutputSchema(format)
			if err != nil {
				return result, err
			}
		}
		result.ToolChoice, result.ToolName, result.ParallelTools, err = openAIToolChoice(object)
		if err != nil {
			return result, err
		}
		if input, ok := object["input"].(string); ok {
			result.Messages = append(result.Messages, canonicalMessage{Role: "user", Parts: []canonicalPart{{Kind: "text", Text: input}}})
		} else {
			for _, item := range array(object["input"]) {
				value := objectMap(item)
				switch stringValue(value["type"]) {
				case "message", "":
					role := stringValue(value["role"])
					parts, err := decodeResponsesContent(value["content"])
					if err != nil {
						return result, err
					}
					if role == "developer" || role == "system" {
						if hasNonText(parts) {
							return result, errors.New("instructions must contain text when translated")
						}
						result.System = appendSystem(result.System, canonicalText(parts))
					} else {
						result.Messages = append(result.Messages, canonicalMessage{Role: role, Parts: parts})
					}
				case "function_call_output":
					result.Messages = append(result.Messages, canonicalMessage{Role: "tool", Parts: []canonicalPart{{Kind: "tool_result", ID: stringValue(value["call_id"]), Text: textValue(value["output"])}}})
				case "function_call":
					var arguments any
					if json.Unmarshal([]byte(stringValue(value["arguments"])), &arguments) != nil {
						return result, errors.New("function call arguments must be valid JSON")
					}
					result.Messages = append(result.Messages, canonicalMessage{Role: "assistant", Parts: []canonicalPart{{Kind: "tool_use", ID: stringValue(value["call_id"]), Name: stringValue(value["name"]), Value: arguments}}})
				default:
					return result, fmt.Errorf("Responses input type %q is not supported for translation", stringValue(value["type"]))
				}
			}
		}
		for _, item := range array(object["tools"]) {
			tool := objectMap(item)
			if stringValue(tool["type"]) != "function" {
				return result, errors.New("only function tools can be translated")
			}
			strict, _ := tool["strict"].(bool)
			result.Tools = append(result.Tools, canonicalTool{Name: stringValue(tool["name"]), Description: stringValue(tool["description"]), Schema: tool["parameters"], Strict: strict})
		}
	case "gemini":
		if err := rejectPresent(object, "cachedContent", "safetySettings"); err != nil {
			return result, err
		}
		systemPartsValue := objectMap(object["systemInstruction"])["parts"]
		if err := validateGeminiParts(systemPartsValue); err != nil {
			return result, errors.New("Gemini systemInstruction must be translatable text")
		}
		systemParts := decodeGeminiParts(systemPartsValue)
		if hasNonText(systemParts) {
			return result, errors.New("Gemini systemInstruction must contain text")
		}
		result.System = canonicalText(systemParts)
		config := objectMap(object["generationConfig"])
		if err := rejectPresent(config, "thinkingConfig", "responseModalities"); err != nil {
			return result, err
		}
		if err := requireSingleCandidate(config, "candidateCount"); err != nil {
			return result, err
		}
		result.Temperature, err = optionalNumber(config, "temperature")
		if err != nil {
			return result, err
		}
		result.TopP, err = optionalNumber(config, "topP")
		if err != nil {
			return result, err
		}
		result.TopK, err = optionalPositiveInteger(config, "topK")
		if err != nil {
			return result, err
		}
		result.MaxTokens, err = optionalInteger(config, "maxOutputTokens")
		if err != nil {
			return result, err
		}
		result.Stops, err = stopValues(config["stopSequences"])
		if err != nil {
			return result, err
		}
		if schema := config["responseJsonSchema"]; schema != nil {
			result.OutputSchema, result.OutputSchemaName, result.StrictOutput = schema, "response", true
		} else if schema := config["responseSchema"]; schema != nil {
			result.OutputSchema, result.OutputSchemaName, result.StrictOutput = schema, "response", true
		} else if mime := stringValue(config["responseMimeType"]); mime != "" && mime != "text/plain" {
			return result, errors.New("responseMimeType requires a JSON schema for translated requests")
		}
		result.ToolChoice, result.ToolName, err = geminiToolChoice(object["toolConfig"])
		if err != nil {
			return result, err
		}
		for _, item := range array(object["contents"]) {
			content := objectMap(item)
			role := stringValue(content["role"])
			if role == "model" {
				role = "assistant"
			}
			if role == "" {
				role = "user"
			}
			current := canonicalMessage{Role: role}
			if err := validateGeminiParts(content["parts"]); err != nil {
				return result, err
			}
			current.Parts = decodeGeminiParts(content["parts"])
			result.Messages = append(result.Messages, current)
		}
		for _, group := range array(object["tools"]) {
			for _, item := range array(objectMap(group)["functionDeclarations"]) {
				tool := objectMap(item)
				result.Tools = append(result.Tools, canonicalTool{Name: stringValue(tool["name"]), Description: stringValue(tool["description"]), Schema: tool["parameters"]})
			}
		}
	default:
		return result, errors.New("unsupported client protocol")
	}
	if len(result.Messages) == 0 {
		return result, errors.New("messages or contents are required")
	}
	resolveToolResultNames(&result)
	if err := validateCanonicalRequest(result); err != nil {
		return result, err
	}
	return result, nil
}

func decodeCanonicalResult(protocol string, raw []byte) (canonicalResult, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return canonicalResult{}, err
	}
	var result canonicalResult
	switch protocol {
	case "openai", "openai_compatible":
		result.ID = stringValue(object["id"])
		choices := array(object["choices"])
		if len(choices) == 0 {
			return result, errors.New("upstream response has no choice")
		}
		choice := objectMap(choices[0])
		message := objectMap(choice["message"])
		parts, err := decodeOpenAIContent(message["content"])
		if err != nil {
			return result, err
		}
		result.Parts = append(result.Parts, parts...)
		if message["reasoning"] != nil {
			return result, errors.New("upstream returned provider-affine content that cannot be translated")
		}
		result.Refusal = stringValue(message["refusal"])
		for _, call := range array(message["tool_calls"]) {
			c := objectMap(call)
			f := objectMap(c["function"])
			var args any
			if json.Unmarshal([]byte(stringValue(f["arguments"])), &args) != nil {
				return result, errors.New("upstream tool arguments are invalid")
			}
			result.Parts = append(result.Parts, canonicalPart{Kind: "tool_use", ID: stringValue(c["id"]), Name: stringValue(f["name"]), Value: args})
		}
		result.Stop = stringValue(choice["finish_reason"])
		usage := objectMap(object["usage"])
		result.InputTokens = number(usage["prompt_tokens"])
		result.OutputTokens = number(usage["completion_tokens"])
	case "anthropic":
		result.ID = stringValue(object["id"])
		if err := validateAnthropicParts(object["content"]); err != nil {
			return result, err
		}
		result.Parts = decodeAnthropicParts(object["content"])
		result.Stop = stringValue(object["stop_reason"])
		if result.Stop == "refusal" {
			result.Refusal = canonicalText(result.Parts)
			if result.Refusal == "" {
				result.Refusal = "Request refused by the provider"
			}
		}
		usage := objectMap(object["usage"])
		result.InputTokens = number(usage["input_tokens"])
		result.OutputTokens = number(usage["output_tokens"])
	case "gemini":
		candidates := array(object["candidates"])
		if len(candidates) == 0 {
			if reason := stringValue(objectMap(object["promptFeedback"])["blockReason"]); reason != "" {
				result.Refusal, result.Stop = "Request blocked by provider safety policy: "+reason, reason
				break
			}
			return result, errors.New("upstream response has no candidate")
		}
		candidate := objectMap(candidates[0])
		if err := validateGeminiParts(objectMap(candidate["content"])["parts"]); err != nil {
			return result, err
		}
		result.Parts = decodeGeminiParts(objectMap(candidate["content"])["parts"])
		result.Stop = stringValue(candidate["finishReason"])
		if geminiSafetyStop(result.Stop) {
			result.Refusal = "Request blocked by provider safety policy: " + result.Stop
		}
		usage := objectMap(object["usageMetadata"])
		result.InputTokens = number(usage["promptTokenCount"])
		result.OutputTokens = number(usage["candidatesTokenCount"])
		result.ID = stringValue(object["responseId"])
	default:
		return result, errors.New("unsupported target protocol")
	}
	if len(result.Parts) == 0 && result.Refusal == "" {
		return result, errors.New("upstream response has no supported content")
	}
	if result.ID == "" {
		result.ID = "pag_translated"
	}
	return result, nil
}

func encodeOpenAIMessages(r canonicalRequest) []any {
	out := []any{}
	if r.System != "" {
		out = append(out, map[string]any{"role": "system", "content": r.System})
	}
	for _, m := range r.Messages {
		value := map[string]any{"role": m.Role}
		if content := openAIContent(m.Parts); content != nil {
			value["content"] = content
		}
		if calls := openAIToolCalls(m.Parts); len(calls) > 0 {
			value["tool_calls"] = calls
		}
		for _, p := range m.Parts {
			if p.Kind == "tool_result" {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": p.ID, "content": p.Text})
			}
		}
		if m.Role != "tool" {
			out = append(out, value)
		}
	}
	return out
}
func encodeAnthropicMessages(r canonicalRequest) []any {
	out := []any{}
	for _, m := range r.Messages {
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		out = append(out, map[string]any{"role": role, "content": anthropicParts(m.Parts)})
	}
	return out
}
func encodeGeminiContents(r canonicalRequest) []any {
	out := []any{}
	for _, m := range r.Messages {
		role := m.Role
		if role == "assistant" {
			role = "model"
		} else if role == "tool" {
			role = "user"
		}
		out = append(out, map[string]any{"role": role, "parts": geminiParts(m.Parts)})
	}
	return out
}

func encodeSampling(value map[string]any, request canonicalRequest, temperature, topP, topK string) {
	if request.Temperature != nil {
		value[temperature] = *request.Temperature
	}
	if request.TopP != nil {
		value[topP] = *request.TopP
	}
	if request.TopK != nil && topK != "" {
		value[topK] = *request.TopK
	}
}

func encodeOpenAITools(tools []canonicalTool) []any {
	out := []any{}
	for _, t := range tools {
		function := map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema}
		if t.Strict {
			function["strict"] = true
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	return out
}
func encodeAnthropicTools(tools []canonicalTool) []any {
	out := []any{}
	for _, t := range tools {
		tool := map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema}
		if t.Strict {
			tool["strict"] = true
		}
		out = append(out, tool)
	}
	return out
}
func encodeGeminiTools(tools []canonicalTool) []any {
	out := []any{}
	for _, t := range tools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema})
	}
	return out
}

func encodeOpenAIToolChoice(value map[string]any, request canonicalRequest) {
	if request.ToolName != "" {
		value["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": request.ToolName}}
	} else if request.ToolChoice != "" {
		value["tool_choice"] = request.ToolChoice
	}
	if request.ParallelTools != nil {
		value["parallel_tool_calls"] = *request.ParallelTools
	}
}

func encodeAnthropicToolChoice(value map[string]any, request canonicalRequest) {
	choice := map[string]any{}
	if request.ToolName != "" {
		choice["type"], choice["name"] = "tool", request.ToolName
	} else {
		choice["type"] = map[string]string{"required": "any"}[request.ToolChoice]
		if choice["type"] == "" {
			choice["type"] = request.ToolChoice
		}
	}
	if request.ParallelTools != nil {
		choice["disable_parallel_tool_use"] = !*request.ParallelTools
	}
	if stringValue(choice["type"]) != "" {
		value["tool_choice"] = choice
	}
}

func encodeGeminiToolChoice(value map[string]any, request canonicalRequest) {
	if request.ToolChoice == "" && request.ToolName == "" {
		return
	}
	config := map[string]any{"mode": map[string]string{"auto": "AUTO", "none": "NONE", "required": "ANY"}[request.ToolChoice]}
	if request.ToolName != "" {
		config["mode"], config["allowedFunctionNames"] = "ANY", []string{request.ToolName}
	}
	value["toolConfig"] = map[string]any{"functionCallingConfig": config}
}
func openAIToolCalls(parts []canonicalPart) []any {
	out := []any{}
	for _, p := range parts {
		if p.Kind == "tool_use" {
			args, _ := json.Marshal(p.Value)
			out = append(out, map[string]any{"id": p.ID, "type": "function", "function": map[string]any{"name": p.Name, "arguments": string(args)}})
		}
	}
	return out
}
func anthropicParts(parts []canonicalPart) []any {
	out := []any{}
	for _, p := range parts {
		switch p.Kind {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case "tool_use":
			out = append(out, map[string]any{"type": "tool_use", "id": p.ID, "name": p.Name, "input": p.Value})
		case "tool_result":
			out = append(out, map[string]any{"type": "tool_result", "tool_use_id": p.ID, "content": p.Text})
		case "image":
			source := map[string]any{}
			if p.Data != "" {
				source = map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}
			} else {
				source = map[string]any{"type": "url", "url": p.URL}
			}
			out = append(out, map[string]any{"type": "image", "source": source})
		}
	}
	return out
}
func geminiParts(parts []canonicalPart) []any {
	out := []any{}
	for _, p := range parts {
		switch p.Kind {
		case "text":
			out = append(out, map[string]any{"text": p.Text})
		case "tool_use":
			out = append(out, map[string]any{"functionCall": map[string]any{"name": p.Name, "args": p.Value, "id": p.ID}})
		case "tool_result":
			out = append(out, map[string]any{"functionResponse": map[string]any{"name": p.Name, "response": map[string]any{"result": p.Text}, "id": p.ID}})
		case "image":
			if p.Data != "" {
				out = append(out, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
			} else {
				file := map[string]any{"fileUri": p.URL}
				if p.MediaType != "" {
					file["mimeType"] = p.MediaType
				}
				out = append(out, map[string]any{"fileData": file})
			}
		}
	}
	return out
}
func decodeAnthropicParts(value any) []canonicalPart {
	if text, ok := value.(string); ok {
		return []canonicalPart{{Kind: "text", Text: text}}
	}
	out := []canonicalPart{}
	for _, item := range array(value) {
		part := objectMap(item)
		switch stringValue(part["type"]) {
		case "text":
			out = append(out, canonicalPart{Kind: "text", Text: stringValue(part["text"])})
		case "tool_use":
			out = append(out, canonicalPart{Kind: "tool_use", ID: stringValue(part["id"]), Name: stringValue(part["name"]), Value: part["input"]})
		case "tool_result":
			out = append(out, canonicalPart{Kind: "tool_result", ID: stringValue(part["tool_use_id"]), Text: textValue(part["content"])})
		case "image":
			source := objectMap(part["source"])
			if stringValue(source["type"]) == "base64" {
				out = append(out, canonicalPart{Kind: "image", MediaType: stringValue(source["media_type"]), Data: stringValue(source["data"])})
			} else {
				out = append(out, canonicalPart{Kind: "image", MediaType: stringValue(source["media_type"]), URL: stringValue(source["url"])})
			}
		}
	}
	return out
}
func decodeGeminiParts(value any) []canonicalPart {
	out := []canonicalPart{}
	for _, item := range array(value) {
		part := objectMap(item)
		if text := stringValue(part["text"]); text != "" {
			out = append(out, canonicalPart{Kind: "text", Text: text})
		}
		if call := objectMap(part["functionCall"]); len(call) > 0 {
			name, id := stringValue(call["name"]), stringValue(call["id"])
			if id == "" {
				id = "call_gemini_" + name
			}
			out = append(out, canonicalPart{Kind: "tool_use", ID: id, Name: name, Value: call["args"]})
		}
		if response := objectMap(part["functionResponse"]); len(response) > 0 {
			name, id := stringValue(response["name"]), stringValue(response["id"])
			if id == "" {
				id = "call_gemini_" + name
			}
			out = append(out, canonicalPart{Kind: "tool_result", ID: id, Name: name, Text: string(mustJSON(response["response"]))})
		}
		if image := firstMap(part, "inlineData", "inline_data"); len(image) > 0 {
			out = append(out, canonicalPart{Kind: "image", MediaType: firstString(image, "mimeType", "mime_type"), Data: stringValue(image["data"])})
		}
		if image := firstMap(part, "fileData", "file_data"); len(image) > 0 {
			out = append(out, canonicalPart{Kind: "image", MediaType: firstString(image, "mimeType", "mime_type"), URL: firstString(image, "fileUri", "file_uri")})
		}
	}
	return out
}

func decodeOpenAIContent(value any) ([]canonicalPart, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return []canonicalPart{{Kind: "text", Text: text}}, nil
	}
	out := []canonicalPart{}
	for _, item := range array(value) {
		part := objectMap(item)
		switch stringValue(part["type"]) {
		case "text", "input_text", "output_text":
			out = append(out, canonicalPart{Kind: "text", Text: stringValue(part["text"])})
		case "image_url":
			image := objectMap(part["image_url"])
			parsed, err := canonicalImage(stringValue(image["url"]), "", stringValue(image["detail"]))
			if err != nil {
				return nil, err
			}
			out = append(out, parsed)
		case "input_image":
			if stringValue(part["file_id"]) != "" {
				return nil, errors.New("file_id images require provider affinity and cannot be translated")
			}
			parsed, err := canonicalImage(stringValue(part["image_url"]), "", stringValue(part["detail"]))
			if err != nil {
				return nil, err
			}
			out = append(out, parsed)
		default:
			return nil, fmt.Errorf("content type %q is not supported for translation", stringValue(part["type"]))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("content must be text or supported image input")
	}
	return out, nil
}

func decodeResponsesContent(value any) ([]canonicalPart, error) {
	if text, ok := value.(string); ok {
		return []canonicalPart{{Kind: "text", Text: text}}, nil
	}
	return decodeOpenAIContent(value)
}

func validateAnthropicParts(value any) error {
	if _, ok := value.(string); ok {
		return nil
	}
	for _, item := range array(value) {
		part := objectMap(item)
		switch stringValue(part["type"]) {
		case "text":
		case "tool_use":
			if stringValue(part["id"]) == "" || stringValue(part["name"]) == "" {
				return errors.New("tool_use requires id and name")
			}
		case "tool_result":
			if stringValue(part["tool_use_id"]) == "" {
				return errors.New("tool_result requires tool_use_id")
			}
		case "image":
			source := objectMap(part["source"])
			switch stringValue(source["type"]) {
			case "base64":
				mediaType, data := stringValue(source["media_type"]), stringValue(source["data"])
				if !strings.HasPrefix(mediaType, "image/") || data == "" {
					return errors.New("base64 image requires media_type and data")
				}
				if _, err := base64.StdEncoding.DecodeString(data); err != nil {
					return errors.New("image contains invalid base64")
				}
			case "url":
				if stringValue(source["url"]) == "" {
					return errors.New("URL image requires url")
				}
			default:
				return errors.New("unsupported Anthropic image source")
			}
		default:
			return fmt.Errorf("Anthropic content type %q requires provider affinity", stringValue(part["type"]))
		}
	}
	return nil
}

func validateGeminiParts(value any) error {
	for _, item := range array(value) {
		part := objectMap(item)
		if part["thought"] != nil || part["thoughtSignature"] != nil || part["thought_signature"] != nil {
			return errors.New("Gemini thought signatures require provider affinity")
		}
		known := part["text"] != nil || len(objectMap(part["functionCall"])) > 0 || len(objectMap(part["functionResponse"])) > 0 || len(firstMap(part, "inlineData", "inline_data")) > 0 || len(firstMap(part, "fileData", "file_data")) > 0
		if !known {
			return errors.New("Gemini content part is not supported for translation")
		}
		if image := firstMap(part, "inlineData", "inline_data"); len(image) > 0 {
			mediaType, data := firstString(image, "mimeType", "mime_type"), stringValue(image["data"])
			if !strings.HasPrefix(mediaType, "image/") || data == "" {
				return errors.New("Gemini inline image requires an image MIME type and data")
			}
			if _, err := base64.StdEncoding.DecodeString(data); err != nil {
				return errors.New("Gemini inline image contains invalid base64")
			}
		}
	}
	return nil
}

func canonicalImage(value, mediaType, detail string) (canonicalPart, error) {
	if value == "" {
		return canonicalPart{}, errors.New("image URL is required")
	}
	if strings.HasPrefix(value, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
		if !ok || !strings.HasSuffix(header, ";base64") || data == "" {
			return canonicalPart{}, errors.New("image data URL must contain base64 data")
		}
		mediaType = strings.TrimSuffix(header, ";base64")
		if !strings.HasPrefix(mediaType, "image/") {
			return canonicalPart{}, errors.New("image data URL must use an image media type")
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return canonicalPart{}, errors.New("image data URL contains invalid base64")
		}
		return canonicalPart{Kind: "image", MediaType: mediaType, Data: data, Detail: detail}, nil
	}
	return canonicalPart{Kind: "image", MediaType: mediaType, URL: value, Detail: detail}, nil
}

func hasNonText(parts []canonicalPart) bool {
	for _, part := range parts {
		if part.Kind != "text" {
			return true
		}
	}
	return false
}

func hasPartKind(parts []canonicalPart, kind string) bool {
	for _, part := range parts {
		if part.Kind == kind {
			return true
		}
	}
	return false
}
func canonicalText(parts []canonicalPart) string {
	result := ""
	for _, p := range parts {
		if p.Kind == "text" {
			result += p.Text
		}
	}
	return result
}

func appendSystem(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n" + next
}

func openAIContent(parts []canonicalPart) any {
	if !hasNonText(parts) {
		return canonicalText(parts)
	}
	out := []any{}
	for _, part := range parts {
		switch part.Kind {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": part.Text})
		case "image":
			imageURL := part.URL
			if part.Data != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			image := map[string]any{"url": imageURL}
			if part.Detail != "" {
				image["detail"] = part.Detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": image})
		}
	}
	return out
}
func geminiText(value any) string { return canonicalText(decodeGeminiParts(value)) }
func textValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	result := ""
	for _, item := range array(value) {
		part := objectMap(item)
		if kind := stringValue(part["type"]); kind == "text" || kind == "input_text" {
			result += stringValue(part["text"])
		}
	}
	return result
}
func objectMap(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any              { result, _ := value.([]any); return result }
func stringValue(value any) string       { result, _ := value.(string); return result }
func number(value any) int64             { result, _ := value.(float64); return int64(result) }
func mustJSON(value any) []byte          { result, _ := json.Marshal(value); return result }

func firstMap(object map[string]any, names ...string) map[string]any {
	for _, name := range names {
		if result := objectMap(object[name]); len(result) > 0 {
			return result
		}
	}
	return nil
}

func firstString(object map[string]any, names ...string) string {
	for _, name := range names {
		if result := stringValue(object[name]); result != "" {
			return result
		}
	}
	return ""
}

func rejectPresent(object map[string]any, fields ...string) error {
	for _, field := range fields {
		if value, exists := object[field]; exists && value != nil {
			return fmt.Errorf("%s requires provider affinity and cannot be translated", field)
		}
	}
	return nil
}

func openAIOutputSchema(value any) (any, string, bool, error) {
	format := objectMap(value)
	if len(format) == 0 {
		return nil, "", false, nil
	}
	if stringValue(format["type"]) != "json_schema" {
		return nil, "", false, errors.New("only json_schema structured output can be translated")
	}
	details := objectMap(format["json_schema"])
	if len(details) == 0 {
		details = format
	}
	schema := details["schema"]
	if schema == nil {
		return nil, "", false, errors.New("json_schema output requires schema")
	}
	name := stringValue(details["name"])
	if name == "" {
		name = "response"
	}
	strict, _ := details["strict"].(bool)
	return schema, name, strict, nil
}

func openAIToolChoice(object map[string]any) (string, string, *bool, error) {
	var parallel *bool
	if value, exists := object["parallel_tool_calls"]; exists {
		allowed, ok := value.(bool)
		if !ok {
			return "", "", nil, errors.New("parallel_tool_calls must be boolean")
		}
		parallel = &allowed
	}
	value := object["tool_choice"]
	if value == nil {
		return "", "", parallel, nil
	}
	if choice, ok := value.(string); ok {
		if choice != "auto" && choice != "none" && choice != "required" {
			return "", "", nil, errors.New("unsupported tool_choice")
		}
		return choice, "", parallel, nil
	}
	choice := objectMap(value)
	if stringValue(choice["type"]) != "function" {
		return "", "", nil, errors.New("only function tool_choice can be translated")
	}
	name := stringValue(objectMap(choice["function"])["name"])
	if name == "" {
		return "", "", nil, errors.New("function tool_choice requires name")
	}
	return "required", name, parallel, nil
}

func anthropicToolChoice(value any) (string, string, *bool, error) {
	if value == nil {
		return "", "", nil, nil
	}
	choice := objectMap(value)
	typeName := stringValue(choice["type"])
	if typeName != "auto" && typeName != "none" && typeName != "any" && typeName != "tool" {
		return "", "", nil, errors.New("unsupported Anthropic tool_choice")
	}
	var parallel *bool
	if value, exists := choice["disable_parallel_tool_use"]; exists {
		disabled, ok := value.(bool)
		if !ok {
			return "", "", nil, errors.New("disable_parallel_tool_use must be boolean")
		}
		allowed := !disabled
		parallel = &allowed
	}
	if typeName == "tool" {
		name := stringValue(choice["name"])
		if name == "" {
			return "", "", nil, errors.New("Anthropic tool choice requires name")
		}
		return "required", name, parallel, nil
	}
	if typeName == "any" {
		typeName = "required"
	}
	return typeName, "", parallel, nil
}

func geminiToolChoice(value any) (string, string, error) {
	if value == nil {
		return "", "", nil
	}
	config := firstMap(objectMap(value), "functionCallingConfig", "function_calling_config")
	mode := strings.ToUpper(firstString(config, "mode"))
	choice := map[string]string{"AUTO": "auto", "NONE": "none", "ANY": "required"}[mode]
	if choice == "" {
		return "", "", errors.New("unsupported Gemini function calling mode")
	}
	allowed := array(config["allowedFunctionNames"])
	if len(allowed) == 0 {
		allowed = array(config["allowed_function_names"])
	}
	if len(allowed) > 1 {
		return "", "", errors.New("multiple allowed Gemini functions cannot be preserved by translation")
	}
	name := ""
	if len(allowed) == 1 {
		name = stringValue(allowed[0])
		if name == "" {
			return "", "", errors.New("allowed Gemini function name must be a string")
		}
	}
	return choice, name, nil
}

func validateCanonicalRequest(request canonicalRequest) error {
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
			return fmt.Errorf("role %q is not supported for translation", message.Role)
		}
		if len(message.Parts) == 0 {
			return errors.New("message content must not be empty")
		}
		for _, part := range message.Parts {
			switch part.Kind {
			case "tool_use":
				if part.ID == "" || part.Name == "" {
					return errors.New("tool calls require id and name")
				}
			case "tool_result":
				if part.ID == "" {
					return errors.New("tool results require a matching tool call id")
				}
			}
		}
	}
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.Schema == nil {
			return errors.New("function tools require name and parameters")
		}
	}
	return nil
}

func resolveToolResultNames(request *canonicalRequest) {
	names := map[string]string{}
	for messageIndex := range request.Messages {
		for partIndex := range request.Messages[messageIndex].Parts {
			part := &request.Messages[messageIndex].Parts[partIndex]
			if part.Kind == "tool_use" {
				names[part.ID] = part.Name
			} else if part.Kind == "tool_result" && part.Name == "" {
				part.Name = names[part.ID]
			}
		}
	}
}

func optionalInteger(object map[string]any, field string) (int64, error) {
	value, exists := object[field]
	if !exists || value == nil {
		return 0, nil
	}
	number, ok := value.(float64)
	if !ok || number < 0 || math.Trunc(number) != number || number > math.MaxInt64 {
		return 0, fmt.Errorf("%s must be a non-negative integer", field)
	}
	return int64(number), nil
}

func optionalPositiveInteger(object map[string]any, field string) (*int64, error) {
	if _, exists := object[field]; !exists || object[field] == nil {
		return nil, nil
	}
	value, err := optionalInteger(object, field)
	if err != nil || value == 0 {
		return nil, fmt.Errorf("%s must be a positive integer", field)
	}
	return &value, nil
}

func optionalNumber(object map[string]any, field string) (*float64, error) {
	value, exists := object[field]
	if !exists || value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil, fmt.Errorf("%s must be a non-negative number", field)
	}
	return &number, nil
}

func requireSingleCandidate(object map[string]any, field string) error {
	if _, exists := object[field]; !exists || object[field] == nil {
		return nil
	}
	value, err := optionalInteger(object, field)
	if err != nil || value != 1 {
		return fmt.Errorf("%s must be 1 for translated requests", field)
	}
	return nil
}

func stopValues(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if item, ok := value.(string); ok {
		if item == "" {
			return nil, errors.New("stop sequence must not be empty")
		}
		return []string{item}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("stop must be a string or array of strings")
	}
	result := make([]string, 0, len(items))
	for _, value := range items {
		item, ok := value.(string)
		if !ok || item == "" {
			return nil, errors.New("stop sequences must be non-empty strings")
		}
		result = append(result, item)
	}
	return result, nil
}
func openAIStop(value string) string {
	if value == "refusal" || geminiSafetyStop(value) {
		return "content_filter"
	}
	if value == "tool_use" || value == "TOOL_CALL" {
		return "tool_calls"
	}
	if value == "max_tokens" || value == "MAX_TOKENS" {
		return "length"
	}
	return "stop"
}
func anthropicStop(value string) string {
	if value == "content_filter" || value == "refusal" || geminiSafetyStop(value) {
		return "refusal"
	}
	if value == "tool_calls" || value == "TOOL_CALL" {
		return "tool_use"
	}
	if value == "length" || value == "MAX_TOKENS" {
		return "max_tokens"
	}
	return "end_turn"
}
func geminiStop(value string) string {
	if value == "content_filter" || value == "refusal" || geminiSafetyStop(value) {
		return "SAFETY"
	}
	if value == "length" || value == "max_tokens" {
		return "MAX_TOKENS"
	}
	return "STOP"
}

func geminiSafetyStop(value string) bool {
	switch strings.ToUpper(value) {
	case "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "RECITATION":
		return true
	default:
		return false
	}
}
