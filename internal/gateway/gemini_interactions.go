package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxSafeJSONInteger = int64(9_007_199_254_740_991)

func validateGeminiInteractionRequest(envelope map[string]json.RawMessage) (int64, error) {
	allowed := map[string]bool{"model": true, "input": true, "store": true, "stream": true, "background": true, "tools": true, "generation_config": true}
	for name := range envelope {
		if !allowed[name] {
			return 0, fmt.Errorf("%s is not supported for stateless text interactions", name)
		}
	}
	var input string
	if json.Unmarshal(envelope["input"], &input) != nil || strings.TrimSpace(input) == "" {
		return 0, errors.New("input must be a non-empty string")
	}
	for _, name := range []string{"store", "stream", "background"} {
		enabled, err := jsonBoolean(envelope, name, false)
		if err != nil {
			return 0, err
		}
		if enabled {
			return 0, fmt.Errorf("%s is not supported for stateless interactions", name)
		}
	}
	if raw, exists := envelope["tools"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || len(tools) != 0 {
			return 0, errors.New("tools are not supported for stateless text interactions")
		}
	}
	raw, exists := envelope["generation_config"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, nil
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return 0, errors.New("generation_config must be an object")
	}
	for name := range config {
		if name != "max_output_tokens" {
			return 0, fmt.Errorf("generation_config.%s is not supported", name)
		}
	}
	var maximum int64
	if rawMaximum, exists := config["max_output_tokens"]; exists {
		if json.Unmarshal(rawMaximum, &maximum) != nil || maximum < 1 || maximum > maxSafeJSONInteger {
			return 0, errors.New("generation_config.max_output_tokens must be a positive integer")
		}
	}
	return maximum, nil
}

func validateGeminiInteractionResponse(raw []byte, publicID string) ([]byte, error) {
	var response struct {
		ID     string                     `json:"id"`
		Object string                     `json:"object"`
		Status string                     `json:"status"`
		Model  string                     `json:"model"`
		Steps  json.RawMessage            `json:"steps"`
		Usage  map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("provider returned an invalid Interaction response")
	}
	if response.ID == "" || response.Object != "interaction" || response.Status != "completed" && response.Status != "incomplete" || response.Model == "" {
		return nil, errors.New("provider returned an invalid Interaction response")
	}
	if len(response.Steps) > 0 && !bytes.Equal(bytes.TrimSpace(response.Steps), []byte("null")) {
		var steps []json.RawMessage
		if json.Unmarshal(response.Steps, &steps) != nil {
			return nil, errors.New("provider returned invalid Interaction steps")
		}
		for _, rawStep := range steps {
			if !validGeminiTextOutputStep(rawStep) {
				return nil, errors.New("provider returned a non-text Interaction step")
			}
		}
	}
	if response.Usage == nil {
		return nil, errors.New("provider returned invalid Interaction usage")
	}
	input, ok := interactionUsageInteger(response.Usage, "total_input_tokens", true)
	if !ok {
		return nil, errors.New("provider returned invalid Interaction input usage")
	}
	output, ok := interactionUsageInteger(response.Usage, "total_output_tokens", true)
	if !ok {
		return nil, errors.New("provider returned invalid Interaction output usage")
	}
	total, ok := interactionUsageInteger(response.Usage, "total_tokens", true)
	if !ok || total < input || output > total-input {
		return nil, errors.New("provider returned inconsistent Interaction usage")
	}
	cache, ok := interactionUsageInteger(response.Usage, "total_cached_tokens", false)
	if !ok || cache > input {
		return nil, errors.New("provider returned invalid Interaction cache usage")
	}
	thought, ok := interactionUsageInteger(response.Usage, "total_thought_tokens", false)
	if !ok {
		return nil, errors.New("provider returned invalid Interaction thought usage")
	}
	tool, ok := interactionUsageInteger(response.Usage, "total_tool_use_tokens", false)
	if !ok || tool != 0 || thought != total-input-output {
		return nil, errors.New("provider returned inconsistent Interaction usage")
	}
	return rewriteResponseModel(raw, publicID)
}

func validGeminiTextOutputStep(raw json.RawMessage) bool {
	var step map[string]json.RawMessage
	var kind string
	if json.Unmarshal(raw, &step) != nil || json.Unmarshal(step["type"], &kind) != nil || kind != "model_output" {
		return false
	}
	var content []json.RawMessage
	if json.Unmarshal(step["content"], &content) != nil {
		return false
	}
	for _, rawItem := range content {
		var item map[string]json.RawMessage
		var itemType, text string
		if json.Unmarshal(rawItem, &item) != nil || json.Unmarshal(item["type"], &itemType) != nil || itemType != "text" || json.Unmarshal(item["text"], &text) != nil {
			return false
		}
	}
	return true
}

func interactionUsageInteger(usage map[string]json.RawMessage, name string, required bool) (int64, bool) {
	raw, exists := usage[name]
	if !exists {
		return 0, !required
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var value int64
	return value, json.Unmarshal(raw, &value) == nil && value >= 0 && value <= maxSafeJSONInteger
}
