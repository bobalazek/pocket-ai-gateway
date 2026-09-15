package gateway

import "encoding/json"

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
			var source map[string]any
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
