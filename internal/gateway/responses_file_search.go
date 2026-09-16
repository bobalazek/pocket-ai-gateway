package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	responseFileSearchScope        = "responses:file_search"
	responseFileSearchFunctionName = "pocket_ai_gateway_file_search"
	responseFileSearchMaxCalls     = int64(4)
)

type responseFileSearchRequest struct {
	enabled        bool
	vectorStoreIDs []string
	maxResults     int
	filter         *vectorStoreAttributeFilter
	scoreThreshold float64
	maxCalls       int64
	maxOutput      int64
	includeResults bool
}

func containsHostedFileSearchTool(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind == "file_search" {
			return true
		}
	}
	return false
}

func validateResponseFileSearch(envelope map[string]json.RawMessage) (responseFileSearchRequest, error) {
	result := responseFileSearchRequest{maxResults: 10, maxCalls: 1}
	rawTools, exists := envelope["tools"]
	if !exists || bytes.Equal(bytes.TrimSpace(rawTools), []byte("null")) {
		return result, nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(rawTools, &tools) != nil {
		return result, errors.New("tools must be an array")
	}
	var webSearch, reservedName bool
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
			var name string
			_ = json.Unmarshal(tool["name"], &name)
			reservedName = reservedName || name == responseFileSearchFunctionName
		case "web_search":
			webSearch = true
		case "file_search":
			if result.enabled {
				return result, errors.New("at most one file_search tool is supported")
			}
			if err := parseResponseFileSearchTool(tool, &result); err != nil {
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
	if webSearch {
		return result, errors.New("file search and web search cannot be combined")
	}
	if reservedName {
		return result, fmt.Errorf("function name %q is reserved for file search", responseFileSearchFunctionName)
	}
	if responseFileSearchUsesReservedChoice(envelope["tool_choice"]) {
		return result, fmt.Errorf("function name %q is reserved for file search", responseFileSearchFunctionName)
	}
	if raw, exists := envelope["stream"]; exists {
		trimmed := bytes.TrimSpace(raw)
		if !bytes.Equal(trimmed, []byte("false")) && !bytes.Equal(trimmed, []byte("null")) {
			return result, errors.New("streaming file search Responses are not supported")
		}
	}
	for _, field := range []string{"conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) {
			return result, fmt.Errorf("%s is not supported with file search", field)
		}
	}
	for _, field := range []string{"prompt", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention"} {
		if raw, exists := envelope[field]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return result, fmt.Errorf("%s is not supported with file search", field)
		}
	}
	if raw, exists := envelope["max_tool_calls"]; exists {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result.maxCalls) != nil || result.maxCalls < 1 || result.maxCalls > responseFileSearchMaxCalls {
			return result, errors.New("max_tool_calls must be an integer between 1 and 4")
		}
	}
	if err := requiredBoundedInteger(envelope, "max_output_tokens", 1, 9_007_199_254_740_991, &result.maxOutput); err != nil {
		return result, err
	}
	if raw, exists := envelope["include"]; exists {
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			return result, errors.New("include must be an array of strings")
		}
		for _, value := range values {
			if value != "file_search_call.results" {
				return result, errors.New("only file_search_call.results may be included with file search")
			}
			result.includeResults = true
		}
	}
	return result, nil
}

func responseFileSearchUsesReservedChoice(raw json.RawMessage) bool {
	var choice map[string]json.RawMessage
	if json.Unmarshal(raw, &choice) != nil {
		return false
	}
	var kind, name string
	_ = json.Unmarshal(choice["type"], &kind)
	_ = json.Unmarshal(choice["name"], &name)
	if kind == "function" && name == responseFileSearchFunctionName {
		return true
	}
	if kind != "allowed_tools" {
		return false
	}
	var tools []map[string]json.RawMessage
	_ = json.Unmarshal(choice["tools"], &tools)
	for _, tool := range tools {
		var toolKind, toolName string
		_ = json.Unmarshal(tool["type"], &toolKind)
		_ = json.Unmarshal(tool["name"], &toolName)
		if toolKind == "function" && toolName == responseFileSearchFunctionName {
			return true
		}
	}
	return false
}

func parseResponseFileSearchTool(tool map[string]json.RawMessage, result *responseFileSearchRequest) error {
	allowed := map[string]bool{"type": true, "vector_store_ids": true, "max_num_results": true, "filters": true, "ranking_options": true}
	for name := range tool {
		if !allowed[name] {
			return fmt.Errorf("file_search.%s is not supported", name)
		}
	}
	if json.Unmarshal(tool["vector_store_ids"], &result.vectorStoreIDs) != nil || len(result.vectorStoreIDs) < 1 || len(result.vectorStoreIDs) > 10 {
		return errors.New("file_search.vector_store_ids must contain 1 to 10 strings")
	}
	seen := make(map[string]bool, len(result.vectorStoreIDs))
	for _, id := range result.vectorStoreIDs {
		if id == "" || seen[id] {
			return errors.New("file_search.vector_store_ids must contain 1 to 10 unique non-empty strings")
		}
		seen[id] = true
	}
	if raw := tool["max_num_results"]; len(raw) > 0 {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result.maxResults) != nil || result.maxResults < 1 || result.maxResults > 50 {
			return errors.New("file_search.max_num_results must be an integer between 1 and 50")
		}
	}
	if raw := tool["filters"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		filter, err := parseVectorStoreAttributeFilter(raw, 0)
		if err != nil {
			return fmt.Errorf("file_search filters: %w", err)
		}
		result.filter = &filter
	}
	rawRanking := tool["ranking_options"]
	if len(rawRanking) == 0 || bytes.Equal(bytes.TrimSpace(rawRanking), []byte("null")) {
		return nil
	}
	var ranking map[string]json.RawMessage
	if json.Unmarshal(rawRanking, &ranking) != nil || ranking == nil || !onlyJSONFields(rawRanking, "ranker", "score_threshold", "hybrid_search") {
		return errors.New("file_search.ranking_options is invalid")
	}
	if _, exists := ranking["hybrid_search"]; exists {
		return errors.New("file_search.ranking_options.hybrid_search is not supported")
	}
	if raw := ranking["ranker"]; len(raw) > 0 {
		var ranker string
		if json.Unmarshal(raw, &ranker) != nil || ranker != "auto" && ranker != "default-2024-11-15" {
			return errors.New("file_search.ranking_options.ranker is invalid")
		}
		return errVectorStoreSemanticRanker
	}
	if raw := ranking["score_threshold"]; len(raw) > 0 {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result.scoreThreshold) != nil || math.IsNaN(result.scoreThreshold) || math.IsInf(result.scoreThreshold, 0) || result.scoreThreshold < 0 || result.scoreThreshold > 1 {
			return errors.New("file_search.ranking_options.score_threshold must be between 0 and 1")
		}
	}
	return nil
}
