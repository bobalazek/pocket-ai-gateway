package protocol

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

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
