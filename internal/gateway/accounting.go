package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strconv"
)

func parseUsage(dialect string, raw []byte) (*int64, *int64, *int64) {
	usage := parseUsageDetails(dialect, raw)
	return usage.inputTokens, usage.outputTokens, nil
}

type parsedUsage struct {
	inputTokens              *int64
	outputTokens             *int64
	cacheCreationInputTokens *int64
	cacheReadInputTokens     *int64
	cacheCreation5mTokens    *int64
	cacheCreation1hTokens    *int64
}

func parseUsageDetails(dialect string, raw []byte) parsedUsage {
	var input, output int64
	foundInput, foundOutput := false, false
	var cacheCreation, cacheRead int64
	foundCacheCreation, foundCacheRead := false, false
	var cacheCreation5m, cacheCreation1h int64
	foundCacheCreation5m, foundCacheCreation1h := false, false
	invalidCache, invalidUsage := false, false
	for _, object := range responseObjects(raw) {
		var value map[string]any
		if json.Unmarshal(object, &value) != nil {
			continue
		}
		var usageMap map[string]any
		geminiInteractionUsage := false
		if dialect == "gemini" {
			usageMap, _ = value["usageMetadata"].(map[string]any)
			if usageMap == nil {
				usageMap, _ = value["usage"].(map[string]any)
				geminiInteractionUsage = usageMap != nil
			}
		} else {
			usageMap, _ = value["usage"].(map[string]any)
			if usageMap == nil {
				usageMap, _ = objectMap(value["response"])["usage"].(map[string]any)
			}
		}
		if dialect == "anthropic" && usageMap == nil {
			if message, _ := value["message"].(map[string]any); message != nil {
				usageMap, _ = message["usage"].(map[string]any)
			}
		}
		if usageMap == nil {
			if dialect == "anthropic" {
				if number, ok := integer(value["input_tokens"]); ok {
					input, foundInput, output, foundOutput = number, true, 0, true
				}
			} else if dialect == "gemini" {
				if number, ok := integer(value["totalTokens"]); ok {
					input, foundInput, output, foundOutput = number, true, 0, true
				}
			}
			continue
		}
		var inputName, outputName string
		if dialect == "gemini" {
			inputName, outputName = "promptTokenCount", "candidatesTokenCount"
			if geminiInteractionUsage {
				inputName, outputName = "total_input_tokens", "total_output_tokens"
			}
		} else if dialect == "anthropic" {
			inputName, outputName = "input_tokens", "output_tokens"
		} else {
			inputName, outputName = "prompt_tokens", "completion_tokens"
			if _, ok := usageMap[inputName]; !ok {
				inputName = "input_tokens"
			}
			if _, ok := usageMap[outputName]; !ok {
				outputName = "output_tokens"
			}
		}
		if number, ok := integer(usageMap[inputName]); ok {
			input, foundInput = number, true
		}
		if number, ok := integer(usageMap[outputName]); ok {
			output, foundOutput = number, true
		}
		if dialect == "anthropic" {
			if number, ok := integer(usageMap["cache_creation_input_tokens"]); ok {
				cacheCreation, foundCacheCreation = number, true
			}
			if number, ok := integer(usageMap["cache_read_input_tokens"]); ok {
				cacheRead, foundCacheRead = number, true
			}
			if details, _ := usageMap["cache_creation"].(map[string]any); details != nil {
				if number, ok := integer(details["ephemeral_5m_input_tokens"]); ok {
					cacheCreation5m, foundCacheCreation5m = number, true
				}
				if number, ok := integer(details["ephemeral_1h_input_tokens"]); ok {
					cacheCreation1h, foundCacheCreation1h = number, true
				}
			}
		} else if dialect == "gemini" {
			cacheName := "cachedContentTokenCount"
			if geminiInteractionUsage {
				cacheName = "total_cached_tokens"
			}
			if rawCache, exists := usageMap[cacheName]; exists {
				if number, ok := integer(rawCache); ok {
					cacheRead, foundCacheRead = number, true
				} else {
					invalidCache = true
				}
			}
			if geminiInteractionUsage {
				total, ok := integer(usageMap["total_tokens"])
				if !ok || !foundInput || !foundOutput || total < input || output > total-input {
					invalidUsage = true
				} else {
					output = total - input
				}
			}
		} else {
			var objectCacheRead int64
			foundObjectCacheRead := false
			setCacheRead := func(rawCache any) {
				number, ok := integer(rawCache)
				if !ok || foundObjectCacheRead && objectCacheRead != number {
					invalidCache = true
					return
				}
				objectCacheRead, foundObjectCacheRead = number, true
			}
			for _, detailsName := range []string{"prompt_tokens_details", "input_tokens_details"} {
				if rawDetails, exists := usageMap[detailsName]; exists && rawDetails != nil {
					details, ok := rawDetails.(map[string]any)
					if !ok {
						invalidCache = true
						continue
					}
					if rawCache, exists := details["cached_tokens"]; exists {
						setCacheRead(rawCache)
					}
				}
			}
			if rawCache, exists := usageMap["prompt_cache_hit_tokens"]; exists {
				setCacheRead(rawCache)
			}
			if rawMiss, exists := usageMap["prompt_cache_miss_tokens"]; exists {
				miss, ok := integer(rawMiss)
				currentInput, hasCurrentInput := integer(usageMap[inputName])
				if !ok || !hasCurrentInput || miss > currentInput {
					invalidCache = true
				} else if foundObjectCacheRead {
					invalidCache = invalidCache || objectCacheRead > currentInput-miss || objectCacheRead+miss != currentInput
				} else {
					objectCacheRead, foundObjectCacheRead = currentInput-miss, true
				}
			}
			if foundObjectCacheRead {
				cacheRead, foundCacheRead = objectCacheRead, true
			}
		}
	}
	if !foundInput || !foundOutput {
		if dialect == "openai" && foundInput {
			for _, object := range responseObjects(raw) {
				var value map[string]any
				_ = json.Unmarshal(object, &value)
				usageMap, _ := value["usage"].(map[string]any)
				if total, ok := integer(usageMap["total_tokens"]); ok && total >= input {
					output, foundOutput = total-input, true
				}
			}
		}
	}
	if !foundInput || !foundOutput || invalidCache || invalidUsage || dialect != "anthropic" && foundCacheRead && cacheRead > input {
		return parsedUsage{}
	}
	if dialect == "anthropic" {
		if foundCacheCreation5m || foundCacheCreation1h {
			detailTotal := cacheCreation5m + cacheCreation1h
			if detailTotal < cacheCreation5m || detailTotal > 9_007_199_254_740_991 || foundCacheCreation && detailTotal != cacheCreation {
				return parsedUsage{}
			}
			if !foundCacheCreation {
				return parsedUsage{}
			}
		}
		if foundCacheCreation {
			if input > 9_007_199_254_740_991-cacheCreation {
				return parsedUsage{}
			}
			input += cacheCreation
		}
		if foundCacheRead {
			if input > 9_007_199_254_740_991-cacheRead {
				return parsedUsage{}
			}
			input += cacheRead
		}
	}
	result := parsedUsage{inputTokens: &input, outputTokens: &output}
	if foundCacheCreation {
		result.cacheCreationInputTokens = &cacheCreation
	}
	if foundCacheRead {
		result.cacheReadInputTokens = &cacheRead
	}
	if foundCacheCreation5m {
		result.cacheCreation5mTokens = &cacheCreation5m
	}
	if foundCacheCreation1h {
		result.cacheCreation1hTokens = &cacheCreation1h
	}
	return result
}

func countRequestTools(dialect string, raw []byte) int64 {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	if dialect != "gemini" {
		return int64(len(array(value["tools"])))
	}
	var count int64
	for _, group := range array(value["tools"]) {
		count += int64(len(array(objectMap(group)["functionDeclarations"])))
	}
	return count
}

func parseToolMetadata(dialect string, raw []byte) (int64, string) {
	keys := map[string]bool{}
	completed := len(raw) > 0 && bytes.TrimSpace(raw)[0] == '{'
	for _, encoded := range responseObjects(raw) {
		var value map[string]any
		if json.Unmarshal(encoded, &value) != nil {
			continue
		}
		if count, ok := integer(value["_gateway_tool_call_count"]); ok {
			status := stringValue(value["_gateway_tool_call_status"])
			return count, status
		}
		switch dialect {
		case "openai", "openai_compatible":
			for choiceIndex, item := range array(value["choices"]) {
				choice := objectMap(item)
				for callIndex, callValue := range array(objectMap(choice["message"])["tool_calls"]) {
					call := objectMap(callValue)
					keys[firstString(call, "id")+":"+strconv.Itoa(choiceIndex)+":"+strconv.Itoa(callIndex)] = true
				}
				for _, callValue := range array(objectMap(choice["delta"])["tool_calls"]) {
					call := objectMap(callValue)
					keys[strconv.Itoa(choiceIndex)+":"+strconv.FormatInt(number(call["index"]), 10)] = true
				}
			}
			completed = completed || bytes.Contains(raw, []byte("data: [DONE]"))
		case "anthropic":
			for index, partValue := range array(value["content"]) {
				part := objectMap(partValue)
				if stringValue(part["type"]) == "tool_use" {
					keys[firstString(part, "id")+":"+strconv.Itoa(index)] = true
				}
			}
			if stringValue(value["type"]) == "content_block_start" && stringValue(objectMap(value["content_block"])["type"]) == "tool_use" {
				keys[strconv.FormatInt(number(value["index"]), 10)] = true
			}
			completed = completed || stringValue(value["type"]) == "message_stop"
		case "gemini":
			for candidateIndex, candidateValue := range array(value["candidates"]) {
				candidate := objectMap(candidateValue)
				for partIndex, partValue := range array(objectMap(candidate["content"])["parts"]) {
					if len(objectMap(objectMap(partValue)["functionCall"])) > 0 {
						keys[strconv.Itoa(candidateIndex)+":"+strconv.Itoa(partIndex)] = true
					}
				}
				completed = completed || stringValue(candidate["finishReason"]) != ""
			}
		case "responses":
			items := array(value["output"])
			if len(items) == 0 {
				items = array(objectMap(value["response"])["output"])
			}
			if item := objectMap(value["item"]); len(item) > 0 {
				items = append(items, item)
			}
			for index, itemValue := range items {
				item := objectMap(itemValue)
				if kind := stringValue(item["type"]); kind == "function_call" || kind == "file_search_call" {
					key := firstString(item, "id", "call_id")
					if key == "" {
						if outputIndex, ok := integer(value["output_index"]); ok {
							key = "index:" + strconv.FormatInt(outputIndex, 10)
						} else {
							key = "item:" + strconv.Itoa(index)
						}
					}
					keys[key] = true
				}
			}
			completed = completed || stringValue(value["type"]) == "response.completed" || stringValue(objectMap(value["response"])["status"]) == "completed"
		}
	}
	if len(keys) == 0 {
		return 0, "none"
	}
	if completed {
		return int64(len(keys)), "completed"
	}
	return int64(len(keys)), "incomplete"
}

func integer(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number > 9_007_199_254_740_991 || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}

func responseObjects(raw []byte) [][]byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return [][]byte{trimmed}
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxInferenceBody+1)
	var data bytes.Buffer
	var objects [][]byte
	limitReached := false
	flush := func() {
		value := bytes.TrimSpace(data.Bytes())
		if len(value) > 0 && !bytes.Equal(value, []byte("[DONE]")) {
			if len(objects) >= maxConversationStreamFrames {
				limitReached = true
				return
			}
			objects = append(objects, append([]byte(nil), value...))
		}
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			flush()
			if limitReached {
				break
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
	flush()
	return objects
}
