package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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
			return result, errors.New("anthropic system content must be translatable text")
		}
		systemParts := decodeAnthropicParts(object["system"])
		if hasNonText(systemParts) {
			return result, errors.New("anthropic system content must contain text")
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
					return result, fmt.Errorf("responses input type %q is not supported for translation", stringValue(value["type"]))
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
			return result, errors.New("gemini systemInstruction must be translatable text")
		}
		systemParts := decodeGeminiParts(systemPartsValue)
		if hasNonText(systemParts) {
			return result, errors.New("gemini systemInstruction must contain text")
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
		if message["reasoning"] != nil || message["reasoning_content"] != nil || message["reasoning_details"] != nil {
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
					return errors.New("url image requires url")
				}
			default:
				return errors.New("unsupported anthropic image source")
			}
		default:
			return fmt.Errorf("anthropic content type %q requires provider affinity", stringValue(part["type"]))
		}
	}
	return nil
}

func validateGeminiParts(value any) error {
	for _, item := range array(value) {
		part := objectMap(item)
		if part["thought"] != nil || part["thoughtSignature"] != nil || part["thought_signature"] != nil {
			return errors.New("gemini thought signatures require provider affinity")
		}
		known := part["text"] != nil || len(objectMap(part["functionCall"])) > 0 || len(objectMap(part["functionResponse"])) > 0 || len(firstMap(part, "inlineData", "inline_data")) > 0 || len(firstMap(part, "fileData", "file_data")) > 0
		if !known {
			return errors.New("gemini content part is not supported for translation")
		}
		if image := firstMap(part, "inlineData", "inline_data"); len(image) > 0 {
			mediaType, data := firstString(image, "mimeType", "mime_type"), stringValue(image["data"])
			if !strings.HasPrefix(mediaType, "image/") || data == "" {
				return errors.New("gemini inline image requires an image MIME type and data")
			}
			if _, err := base64.StdEncoding.DecodeString(data); err != nil {
				return errors.New("gemini inline image contains invalid base64")
			}
		}
	}
	return nil
}

func canonicalImage(value, mediaType, detail string) (canonicalPart, error) {
	if value == "" {
		return canonicalPart{}, errors.New("image url is required")
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
