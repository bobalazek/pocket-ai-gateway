package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

const (
	webSearchMaxCalls      = int64(4)
	webSearchInputPerCall  = int64(128_000)
	webSearchDomainLimit   = 100
	webSearchDomainMaxSize = 253
)

type responseWebSearchRequest struct {
	enabled  bool
	maxCalls int64
}

type responseWebSearchResult struct {
	status         string
	completedCalls int64
}

func containsHostedWebSearchTool(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind == "web_search" || strings.HasPrefix(kind, "web_search_") {
			return true
		}
	}
	return false
}

func webSearchTargetEligibility(target providers.Target) (bool, string) {
	if !nativeTarget("responses", target.Adapter) || target.Adapter != "openai" || target.Preset != "openai" {
		return false, "web_search_native_required"
	}
	if !slices.Contains(target.Capabilities, "chat") || !slices.Contains(target.UpstreamCapabilities, "chat") || !slices.Contains(target.Capabilities, "web_search") || !slices.Contains(target.UpstreamCapabilities, "web_search") {
		return false, "unsupported_capability"
	}
	if target.RoutingStrategy == "lowest_cost" || target.FreeOnly {
		return false, "web_search_price_contract_unavailable"
	}
	return true, ""
}

func validateResponseWebSearch(envelope map[string]json.RawMessage) (responseWebSearchRequest, error) {
	var result responseWebSearchRequest
	rawTools, exists := envelope["tools"]
	if !exists || bytes.Equal(bytes.TrimSpace(rawTools), []byte("null")) {
		return result, nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(rawTools, &tools) != nil {
		return result, errors.New("tools must be an array")
	}
	for _, rawTool := range tools {
		var tool map[string]json.RawMessage
		if json.Unmarshal(rawTool, &tool) != nil || tool == nil {
			return result, errors.New("tools must contain objects")
		}
		var kind string
		if json.Unmarshal(tool["type"], &kind) != nil || kind == "" {
			return result, errors.New("every tool must have a string type")
		}
		switch kind {
		case "function":
			continue
		case "file_search":
			continue
		case "web_search":
			if result.enabled {
				return result, errors.New("at most one web_search tool is supported")
			}
			if err := validateWebSearchTool(tool); err != nil {
				return result, err
			}
			result.enabled = true
		default:
			return result, errors.New("only function, web_search, and file_search tools are supported by Responses")
		}
	}
	if !result.enabled {
		return result, nil
	}
	if raw, exists := envelope["stream"]; exists {
		trimmed := bytes.TrimSpace(raw)
		if !bytes.Equal(trimmed, []byte("false")) && !bytes.Equal(trimmed, []byte("null")) {
			return result, errors.New("streaming web search Responses are not supported")
		}
	}
	if raw, exists := envelope["conversation"]; exists {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`)) {
			return result, errors.New("conversation is not supported with web search")
		}
	}
	if raw, exists := envelope["previous_response_id"]; exists {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`)) {
			return result, errors.New("previous_response_id is not supported with web search")
		}
	}
	for _, field := range []string{"prompt", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention"} {
		if raw, exists := envelope[field]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return result, fmt.Errorf("%s is not supported with web search", field)
		}
	}
	if err := requiredBoundedInteger(envelope, "max_tool_calls", 1, webSearchMaxCalls, &result.maxCalls); err != nil {
		return result, err
	}
	var maxOutput int64
	if err := requiredBoundedInteger(envelope, "max_output_tokens", 1, 9_007_199_254_740_991, &maxOutput); err != nil {
		return result, err
	}
	if raw, exists := envelope["include"]; exists {
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			return result, errors.New("include must be an array of strings")
		}
		for _, value := range values {
			if value != "web_search_call.action.sources" {
				return result, errors.New("only web_search_call.action.sources may be included with web search")
			}
		}
	}
	if raw := bytes.TrimSpace(envelope["input"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var input any
		if json.Unmarshal(raw, &input) != nil {
			return result, errors.New("input is invalid")
		}
		if err := rejectCompactReferences(input); err != nil {
			return result, err
		}
		if hasOpenAICacheBreakpoint(input) {
			return result, errors.New("prompt_cache_breakpoint is not supported with web search")
		}
	}
	return result, nil
}

func hasOpenAICacheBreakpoint(value any) bool {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if hasOpenAICacheBreakpoint(item) {
				return true
			}
		}
	case map[string]any:
		if breakpoint, exists := value["prompt_cache_breakpoint"]; exists && breakpoint != nil {
			return true
		}
		for _, item := range value {
			if hasOpenAICacheBreakpoint(item) {
				return true
			}
		}
	}
	return false
}

func validateWebSearchTool(tool map[string]json.RawMessage) error {
	allowed := map[string]bool{"type": true, "search_context_size": true, "user_location": true, "filters": true, "external_web_access": true, "return_token_budget": true}
	for name := range tool {
		if !allowed[name] {
			return fmt.Errorf("web_search.%s is not supported", name)
		}
	}
	if raw := bytes.TrimSpace(tool["search_context_size"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value string
		if json.Unmarshal(raw, &value) != nil || value != "low" && value != "medium" && value != "high" {
			return errors.New("web_search.search_context_size must be low, medium, or high")
		}
	}
	if raw := bytes.TrimSpace(tool["external_web_access"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("web_search.external_web_access must be a boolean")
		}
	}
	if raw, exists := tool["return_token_budget"]; exists {
		raw = bytes.TrimSpace(raw)
		var value string
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil || value != "default" {
			return errors.New("web_search.return_token_budget must be default; unlimited is not supported")
		}
	}
	if err := validateApproximateLocation(tool["user_location"]); err != nil {
		return err
	}
	return validateWebSearchFilters(tool["filters"])
}

func validateApproximateLocation(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var location map[string]json.RawMessage
	if json.Unmarshal(trimmed, &location) != nil || location == nil {
		return errors.New("web_search.user_location must be an approximate location object")
	}
	allowed := map[string]bool{"type": true, "city": true, "country": true, "region": true, "timezone": true}
	for name, rawValue := range location {
		if !allowed[name] {
			return fmt.Errorf("web_search.user_location.%s is not supported", name)
		}
		if name == "type" {
			continue
		}
		value := ""
		if !bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) && (json.Unmarshal(rawValue, &value) != nil || value == "" || utf8.RuneCountInString(value) > 255) {
			return fmt.Errorf("web_search.user_location.%s must be a string of at most 255 characters", name)
		}
	}
	var kind string
	if json.Unmarshal(location["type"], &kind) != nil || kind != "approximate" {
		return errors.New("web_search.user_location.type must be approximate")
	}
	return nil
}

func validateWebSearchFilters(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var filters map[string]json.RawMessage
	if json.Unmarshal(trimmed, &filters) != nil || filters == nil {
		return errors.New("web_search.filters must be an object")
	}
	for name, rawDomains := range filters {
		if name != "allowed_domains" && name != "blocked_domains" {
			return fmt.Errorf("web_search.filters.%s is not supported", name)
		}
		var domains []string
		if json.Unmarshal(rawDomains, &domains) != nil || len(domains) > webSearchDomainLimit {
			return fmt.Errorf("web_search.filters.%s must contain at most 100 domains", name)
		}
		for _, domain := range domains {
			if !validWebSearchDomain(domain) {
				return fmt.Errorf("web_search.filters.%s contains an invalid domain", name)
			}
		}
	}
	return nil
}

func validWebSearchDomain(value string) bool {
	if value == "" || len(value) > webSearchDomainMaxSize || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/:@?#\\") {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, character := range part {
			if character < 'a' || character > 'z' {
				if character < 'A' || character > 'Z' {
					if character < '0' || character > '9' {
						if character != '-' {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

func requiredBoundedInteger(envelope map[string]json.RawMessage, name string, minimum, maximum int64, destination *int64) error {
	raw, exists := envelope[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, destination) != nil || *destination < minimum || *destination > maximum {
		return fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return nil
}

func parseWebSearchResponse(raw []byte) (responseWebSearchResult, error) {
	var result responseWebSearchResult
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || response == nil {
		return result, errors.New("web search provider response must be a JSON object")
	}
	if json.Unmarshal(response["status"], &result.status) != nil || result.status != "completed" && result.status != "incomplete" && result.status != "failed" && result.status != "cancelled" {
		return result, errors.New("web search provider response status is invalid")
	}
	var output []json.RawMessage
	if rawOutput, exists := response["output"]; !exists || bytes.Equal(bytes.TrimSpace(rawOutput), []byte("null")) || json.Unmarshal(rawOutput, &output) != nil {
		return result, errors.New("web search provider response output must be an array")
	}
	seen := map[string]bool{}
	for _, rawItem := range output {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) != nil || item == nil {
			return result, errors.New("web search provider response output contains an invalid item")
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		if kind != "web_search_call" {
			continue
		}
		var status string
		if json.Unmarshal(item["status"], &status) != nil {
			return result, errors.New("web search call status is invalid")
		}
		if status != "in_progress" && status != "searching" && status != "completed" && status != "failed" && status != "incomplete" {
			return result, errors.New("web search call status is invalid")
		}
		if status != "completed" {
			continue
		}
		var id string
		if json.Unmarshal(item["id"], &id) != nil || id == "" || len(id) > 200 {
			return result, errors.New("completed web search call id is invalid")
		}
		rawAction := bytes.TrimSpace(item["action"])
		var action map[string]json.RawMessage
		if len(rawAction) == 0 || bytes.Equal(rawAction, []byte("null")) || json.Unmarshal(rawAction, &action) != nil || action == nil {
			return result, errors.New("completed web search call action is invalid")
		}
		var actionType string
		if json.Unmarshal(action["type"], &actionType) != nil || actionType != "search" && actionType != "open_page" && actionType != "find_in_page" {
			return result, errors.New("completed web search call action type is invalid")
		}
		if actionType != "search" {
			continue
		}
		seen[id] = true
	}
	result.completedCalls = int64(len(seen))
	return result, nil
}
