package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

var ErrUpstreamResponseInterrupted = errors.New("upstream response interrupted")

type streamToolDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

type streamDelta struct {
	Text                      string
	Refusal                   string
	Tools                     []streamToolDelta
	Stop                      string
	Done                      bool
	InputTokens, OutputTokens *int64
	Usage                     map[string]any
	InvalidUsage              bool
}

type streamTool struct {
	ID, Name, Arguments string
	OutputIndex         int
}

func TranslateStream(response http.ResponseWriter, source io.Reader, client, target, model string) (int, []byte, error) {
	if client == "anthropic" && target != "anthropic" {
		return http.StatusBadRequest, nil, errors.New("anthropic streaming requires an Anthropic-compatible target")
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		return http.StatusOK, nil, io.ErrUnexpectedEOF
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	const maxStreamBytes = int64(16 << 20)
	var emitErr error
	emit := func(event string, value any) {
		if emitErr != nil {
			return
		}
		encoded, _ := json.Marshal(value)
		var frame bytes.Buffer
		if event != "" {
			frame.WriteString("event: " + event + "\n")
		}
		frame.Write(append(append([]byte("data: "), encoded...), '\n', '\n'))
		_, emitErr = response.Write(frame.Bytes())
		flusher.Flush()
	}

	var data bytes.Buffer
	var outputText bytes.Buffer
	tools := map[int]*streamTool{}
	started, textStarted, completed := false, false, false
	var inputTokens, outputTokens *int64
	usageFields := map[string]any{}
	invalidUsage := false
	terminalSeen, terminalUsage := false, false
	var bytesRead int64
	textOutputIndex := -1
	stop, refusal, nextOutput := "", "", 0
	metadata := func(status string) []byte {
		if len(tools) == 0 {
			status = "none"
		}
		if invalidUsage {
			return usageDocument(target, nil, nil, usageFields, int64(len(tools)), status)
		}
		return usageDocument(target, inputTokens, outputTokens, usageFields, int64(len(tools)), status)
	}
	ensureStarted := func() {
		if started {
			return
		}
		switch client {
		case "openai":
			emit("", map[string]any{"id": "chatcmpl_translated", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
		case "anthropic":
			emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_translated", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
		case "responses":
			emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_translated", "object": "response", "status": "in_progress", "model": model, "output": []any{}}})
		}
		started = true
	}
	startText := func() {
		if textStarted {
			return
		}
		ensureStarted()
		switch client {
		case "anthropic":
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": nextOutput, "content_block": map[string]any{"type": "text", "text": ""}})
		case "responses":
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": nextOutput, "item": map[string]any{"id": "msg_translated", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
			emit("response.content_part.added", map[string]any{"type": "response.content_part.added", "item_id": "msg_translated", "output_index": nextOutput, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
		}
		textStarted = true
		textOutputIndex = nextOutput
		nextOutput++
	}

	consume := func(payload []byte) error {
		delta, err := decodeStreamDelta(target, payload)
		if err != nil {
			return err
		}
		if delta.Text != "" {
			startText()
			if int64(outputText.Len()+len(delta.Text)) > maxStreamBytes {
				return errors.New("translated stream output exceeds 16 MiB")
			}
			outputText.WriteString(delta.Text)
			switch client {
			case "openai":
				emit("", map[string]any{"id": "chatcmpl_translated", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta.Text}, "finish_reason": nil}}})
			case "anthropic":
				emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": textOutputIndex, "delta": map[string]any{"type": "text_delta", "text": delta.Text}})
			case "gemini":
				emit("", map[string]any{"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": delta.Text}}}}}})
			case "responses":
				emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_translated", "output_index": textOutputIndex, "content_index": 0, "delta": delta.Text})
			}
		}
		for _, item := range delta.Tools {
			tool := tools[item.Index]
			if tool == nil {
				ensureStarted()
				tool = &streamTool{ID: item.ID, Name: item.Name, OutputIndex: nextOutput}
				if tool.ID == "" {
					tool.ID = fmt.Sprintf("call_translated_%d", item.Index)
				}
				tools[item.Index] = tool
				nextOutput++
				switch client {
				case "anthropic":
					emit("content_block_start", map[string]any{"type": "content_block_start", "index": tool.OutputIndex, "content_block": map[string]any{"type": "tool_use", "id": tool.ID, "name": tool.Name, "input": map[string]any{}}})
				case "responses":
					emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": tool.OutputIndex, "item": map[string]any{"id": "fc_" + tool.ID, "type": "function_call", "call_id": tool.ID, "name": tool.Name, "arguments": "", "status": "in_progress"}})
				}
			}
			if item.ID != "" {
				tool.ID = item.ID
			}
			if item.Name != "" {
				tool.Name = item.Name
			}
			if int64(len(tool.Arguments)+len(item.Arguments)) > maxStreamBytes {
				return errors.New("translated stream tool arguments exceed 16 MiB")
			}
			tool.Arguments += item.Arguments
			switch client {
			case "openai":
				function := map[string]any{}
				if item.Name != "" {
					function["name"] = item.Name
				}
				if item.Arguments != "" {
					function["arguments"] = item.Arguments
				}
				call := map[string]any{"index": item.Index, "function": function}
				if item.ID != "" {
					call["id"], call["type"] = item.ID, "function"
				}
				emit("", map[string]any{"id": "chatcmpl_translated", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": nil}}})
			case "anthropic":
				if item.Arguments != "" {
					emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.OutputIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": item.Arguments}})
				}
			case "responses":
				if item.Arguments != "" {
					emit("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_" + tool.ID, "output_index": tool.OutputIndex, "delta": item.Arguments})
				}
			}
		}
		if delta.Stop != "" {
			stop = delta.Stop
			terminalSeen = true
		}
		if delta.Refusal != "" {
			refusal += delta.Refusal
		}
		if delta.InputTokens != nil {
			inputTokens = delta.InputTokens
		}
		if delta.OutputTokens != nil {
			outputTokens = delta.OutputTokens
		}
		for name, value := range delta.Usage {
			usageFields[name] = value
		}
		invalidUsage = invalidUsage || delta.InvalidUsage
		switch target {
		case "openai", "openai_compatible":
			terminalUsage = terminalUsage || terminalSeen && delta.InputTokens != nil && delta.OutputTokens != nil
		case "anthropic":
			terminalUsage = terminalUsage || delta.Stop != "" && delta.OutputTokens != nil && inputTokens != nil
		case "gemini":
			terminalUsage = terminalUsage || delta.Done && delta.OutputTokens != nil && inputTokens != nil
		}
		completed = completed || delta.Done
		return emitErr
	}

	scanner := bufio.NewScanner(io.LimitReader(source, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 4096), int(maxStreamBytes)+2)
	for scanner.Scan() {
		line := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		bytesRead += int64(len(line) + 1)
		if bytesRead > maxStreamBytes {
			return http.StatusOK, metadata("incomplete"), errors.New("upstream stream exceeds 16 MiB")
		}
		if len(line) == 0 {
			payload := append([]byte(nil), bytes.TrimSpace(data.Bytes())...)
			data.Reset()
			if len(payload) == 0 {
				continue
			}
			if bytes.Equal(payload, []byte("[DONE]")) {
				completed = true
				continue
			}
			if err := consume(payload); err != nil {
				return http.StatusOK, metadata("incomplete"), err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	if scanner.Err() != nil {
		return http.StatusOK, metadata("incomplete"), fmt.Errorf("%w: %v", ErrUpstreamResponseInterrupted, scanner.Err())
	}
	if data.Len() > 0 {
		if err := consume(bytes.TrimSpace(data.Bytes())); err != nil {
			return http.StatusOK, metadata("incomplete"), err
		}
	}
	if !completed {
		return http.StatusOK, metadata("incomplete"), fmt.Errorf("%w: terminal event missing", ErrUpstreamResponseInterrupted)
	}
	if invalidUsage {
		return http.StatusOK, metadata("incomplete"), errors.New("upstream stream contains invalid token usage")
	}
	if !terminalUsage {
		invalidUsage = true
		return http.StatusOK, metadata("incomplete"), errors.New("upstream stream omitted terminal token usage")
	}
	for _, tool := range tools {
		if tool.Arguments == "" {
			tool.Arguments = "{}"
		}
		if !json.Valid([]byte(tool.Arguments)) {
			return http.StatusOK, metadata("incomplete"), errors.New("upstream tool arguments are invalid")
		}
	}
	if inputTokens == nil || outputTokens == nil {
		return http.StatusOK, metadata("completed"), errors.New("upstream stream omitted required token usage")
	}
	in, out := *inputTokens, *outputTokens
	ensureStarted()
	finishTranslatedStream(client, model, stop, refusal, outputText.String(), textOutputIndex, tools, in, out, emit, response, flusher)
	return http.StatusOK, metadata("completed"), emitErr
}

func finishTranslatedStream(client, model, stop, refusal, outputText string, textOutputIndex int, tools map[int]*streamTool, inputTokens, outputTokens int64, emit func(string, any), response http.ResponseWriter, flusher http.Flusher) {
	switch client {
	case "openai":
		if refusal != "" {
			emit("", map[string]any{"id": "chatcmpl_translated", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"refusal": refusal}, "finish_reason": nil}}})
		}
		emit("", map[string]any{"id": "chatcmpl_translated", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": openAIStop(stop)}}, "usage": map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}})
		_, _ = io.WriteString(response, "data: [DONE]\n\n")
		flusher.Flush()
	case "anthropic":
		if refusal != "" && textOutputIndex < 0 {
			textOutputIndex = 0
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": textOutputIndex, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": textOutputIndex, "delta": map[string]any{"type": "text_delta", "text": refusal}})
		}
		if textOutputIndex >= 0 {
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": textOutputIndex})
		}
		for _, tool := range orderedStreamTools(tools) {
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": tool.OutputIndex})
		}
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": anthropicStop(stop), "stop_sequence": nil}, "usage": map[string]any{"output_tokens": outputTokens}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	case "gemini":
		parts := []any{}
		for _, tool := range orderedStreamTools(tools) {
			var arguments any
			if json.Unmarshal([]byte(tool.Arguments), &arguments) == nil {
				parts = append(parts, map[string]any{"functionCall": map[string]any{"id": tool.ID, "name": tool.Name, "args": arguments}})
			}
		}
		emit("", map[string]any{"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}, "finishReason": geminiStop(stop)}}, "usageMetadata": map[string]any{"promptTokenCount": inputTokens, "candidatesTokenCount": outputTokens, "totalTokenCount": inputTokens + outputTokens}})
	case "responses":
		outputByIndex := map[int]any{}
		messageContent := []any{}
		messageIndex := textOutputIndex
		if refusal != "" && textOutputIndex < 0 {
			messageIndex = 0
			for _, tool := range orderedStreamTools(tools) {
				if tool.OutputIndex >= messageIndex {
					messageIndex = tool.OutputIndex + 1
				}
			}
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": messageIndex, "item": map[string]any{"id": "msg_translated", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
		}
		if textOutputIndex >= 0 {
			emit("response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": "msg_translated", "output_index": textOutputIndex, "content_index": 0, "text": outputText})
			messageContent = append(messageContent, map[string]any{"type": "output_text", "text": outputText, "annotations": []any{}})
		}
		if refusal != "" {
			contentIndex := len(messageContent)
			emit("response.content_part.added", map[string]any{"type": "response.content_part.added", "item_id": "msg_translated", "output_index": messageIndex, "content_index": contentIndex, "part": map[string]any{"type": "refusal", "refusal": ""}})
			emit("response.refusal.delta", map[string]any{"type": "response.refusal.delta", "item_id": "msg_translated", "output_index": messageIndex, "content_index": contentIndex, "delta": refusal})
			emit("response.refusal.done", map[string]any{"type": "response.refusal.done", "item_id": "msg_translated", "output_index": messageIndex, "content_index": contentIndex, "refusal": refusal})
			messageContent = append(messageContent, map[string]any{"type": "refusal", "refusal": refusal})
		}
		if len(messageContent) > 0 {
			item := map[string]any{"id": "msg_translated", "type": "message", "role": "assistant", "status": "completed", "content": messageContent}
			emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": messageIndex, "item": item})
			outputByIndex[messageIndex] = item
		}
		for _, tool := range orderedStreamTools(tools) {
			item := map[string]any{"id": "fc_" + tool.ID, "type": "function_call", "call_id": tool.ID, "name": tool.Name, "arguments": tool.Arguments, "status": "completed"}
			emit("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_" + tool.ID, "output_index": tool.OutputIndex, "arguments": tool.Arguments})
			emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": tool.OutputIndex, "item": item})
			outputByIndex[tool.OutputIndex] = item
		}
		output := make([]any, 0, len(outputByIndex))
		for index := 0; len(output) < len(outputByIndex); index++ {
			if item := outputByIndex[index]; item != nil {
				output = append(output, item)
			}
		}
		emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_translated", "object": "response", "status": "completed", "model": model, "output": output, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}}})
	}
}

func decodeStreamDelta(target string, payload []byte) (streamDelta, error) {
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return streamDelta{}, errors.New("upstream stream contains invalid JSON")
	}
	var result streamDelta
	result.InputTokens, result.OutputTokens, result.Usage, result.InvalidUsage = streamUsage(target, value)
	switch target {
	case "openai", "openai_compatible":
		choices := array(value["choices"])
		if len(choices) == 0 {
			return result, nil
		}
		choice := objectMap(choices[0])
		delta := objectMap(choice["delta"])
		if delta["reasoning"] != nil {
			return result, errors.New("upstream stream contains provider-affine content")
		}
		result.Text = stringValue(delta["content"])
		result.Refusal = stringValue(delta["refusal"])
		for _, item := range array(delta["tool_calls"]) {
			call := objectMap(item)
			function := objectMap(call["function"])
			result.Tools = append(result.Tools, streamToolDelta{Index: int(number(call["index"])), ID: stringValue(call["id"]), Name: stringValue(function["name"]), Arguments: stringValue(function["arguments"])})
		}
		result.Stop = stringValue(choice["finish_reason"])
	case "anthropic":
		typeName := stringValue(value["type"])
		index := int(number(value["index"]))
		switch typeName {
		case "content_block_start":
			block := objectMap(value["content_block"])
			switch stringValue(block["type"]) {
			case "text":
			case "tool_use":
				result.Tools = append(result.Tools, streamToolDelta{Index: index, ID: stringValue(block["id"]), Name: stringValue(block["name"])})
			default:
				return result, errors.New("upstream stream contains provider-affine content")
			}
		case "content_block_delta":
			delta := objectMap(value["delta"])
			switch stringValue(delta["type"]) {
			case "text_delta":
				result.Text = stringValue(delta["text"])
			case "input_json_delta":
				result.Tools = append(result.Tools, streamToolDelta{Index: index, Arguments: stringValue(delta["partial_json"])})
			default:
				return result, errors.New("upstream stream contains unsupported content")
			}
		case "message_delta":
			result.Stop = stringValue(objectMap(value["delta"])["stop_reason"])
			if result.Stop == "refusal" {
				result.Refusal = "Request refused by the provider"
			}
		case "message_stop":
			result.Done = true
		}
	case "gemini":
		candidates := array(value["candidates"])
		if len(candidates) == 0 {
			return result, nil
		}
		candidate := objectMap(candidates[0])
		parts := objectMap(candidate["content"])["parts"]
		if err := validateGeminiParts(parts); err != nil {
			return result, err
		}
		for index, item := range array(parts) {
			part := objectMap(item)
			result.Text += stringValue(part["text"])
			if call := objectMap(part["functionCall"]); len(call) > 0 {
				arguments, _ := json.Marshal(call["args"])
				result.Tools = append(result.Tools, streamToolDelta{Index: index, ID: stringValue(call["id"]), Name: stringValue(call["name"]), Arguments: string(arguments)})
			}
		}
		result.Stop = strings.TrimSpace(stringValue(candidate["finishReason"]))
		if geminiSafetyStop(result.Stop) {
			result.Refusal = "Request blocked by provider safety policy: " + result.Stop
		}
		result.Done = result.Stop != ""
	default:
		return result, errors.New("unsupported target protocol")
	}
	return result, nil
}

func streamUsage(target string, value map[string]any) (*int64, *int64, map[string]any, bool) {
	rawUsage := value["usage"]
	usage := objectMap(rawUsage)
	inputName, outputName := "prompt_tokens", "completion_tokens"
	if target == "anthropic" {
		if rawUsage == nil {
			rawUsage = objectMap(value["message"])["usage"]
			usage = objectMap(rawUsage)
		}
		inputName, outputName = "input_tokens", "output_tokens"
	} else if target == "gemini" {
		rawUsage = value["usageMetadata"]
		usage = objectMap(rawUsage)
		inputName, outputName = "promptTokenCount", "candidatesTokenCount"
	}
	_, usageIsObject := rawUsage.(map[string]any)
	invalid := rawUsage != nil && !usageIsObject
	var input, output *int64
	if raw, exists := usage[inputName]; exists {
		if number, ok := integer(raw); ok {
			input = &number
		} else {
			invalid = true
		}
	}
	if raw, exists := usage[outputName]; exists {
		if number, ok := integer(raw); ok {
			output = &number
		} else {
			invalid = true
		}
	}
	return input, output, usage, invalid
}

func usageDocument(target string, input, output *int64, fields map[string]any, toolCalls int64, toolStatus string) []byte {
	result := map[string]any{"_gateway_tool_call_count": toolCalls, "_gateway_tool_call_status": toolStatus}
	if input == nil || output == nil {
		return mustJSON(result)
	}
	usage := make(map[string]any, len(fields)+2)
	for name, value := range fields {
		usage[name] = value
	}
	switch target {
	case "anthropic":
		usage["input_tokens"], usage["output_tokens"] = *input, *output
		result["usage"] = usage
	case "gemini":
		usage["promptTokenCount"], usage["candidatesTokenCount"] = *input, *output
		result["usageMetadata"] = usage
	default:
		usage["prompt_tokens"], usage["completion_tokens"] = *input, *output
		result["usage"] = usage
	}
	return mustJSON(result)
}

func orderedStreamTools(values map[int]*streamTool) []*streamTool {
	result := make([]*streamTool, 0, len(values))
	indices := make([]int, 0, len(values))
	for index := range values {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result = append(result, values[index])
	}
	return result
}
