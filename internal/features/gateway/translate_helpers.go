package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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
			return "", "", nil, errors.New("anthropic tool choice requires name")
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
