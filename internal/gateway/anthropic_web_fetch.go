package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

const (
	anthropicWebFetchMaxUses          = int64(4)
	anthropicWebFetchMaxContentTokens = int64(16_384)
	anthropicWebFetchDomainLimit      = 10
)

type anthropicWebFetchRequest struct {
	enabled           bool
	dynamic           bool
	cacheBypass       bool
	responseInclusion bool
	maxUses           int64
	maxContentTokens  int64
}

func containsAnthropicWebFetchTool(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind == "web_fetch" || strings.HasPrefix(kind, "web_fetch_") {
			return true
		}
	}
	return false
}

func validateAnthropicWebFetch(envelope map[string]json.RawMessage) (anthropicWebFetchRequest, error) {
	var result anthropicWebFetchRequest
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
		rawType, typed := tool["type"]
		if !typed {
			continue
		}
		var kind string
		if json.Unmarshal(rawType, &kind) != nil || kind == "" {
			return result, errors.New("tool.type must be a non-empty string")
		}
		switch kind {
		case "custom":
			continue
		case "web_search_20250305", "web_search_20260209", "web_search_20260318":
			continue
		case "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309", "web_fetch_20260318":
			if result.enabled {
				return result, errors.New("at most one web_fetch tool is supported")
			}
			result.cacheBypass = kind == "web_fetch_20260309" || kind == "web_fetch_20260318"
			result.responseInclusion = kind == "web_fetch_20260318"
			if err := validateAnthropicWebFetchTool(tool, &result, kind != "web_fetch_20250910", result.cacheBypass, result.responseInclusion); err != nil {
				return result, err
			}
			result.enabled = true
		default:
			return result, fmt.Errorf("server tool type %q is not supported", kind)
		}
	}
	if result.enabled {
		if raw, exists := envelope["stream"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			var stream bool
			if json.Unmarshal(raw, &stream) != nil {
				return result, errors.New("stream must be a boolean or null")
			}
		}
	}
	return result, nil
}

func validateAnthropicWebFetchTool(tool map[string]json.RawMessage, result *anthropicWebFetchRequest, dynamicVersion, cacheBypassVersion, responseInclusionVersion bool) error {
	allowed := map[string]bool{
		"type": true, "name": true, "max_uses": true, "max_content_tokens": true,
		"allowed_domains": true, "blocked_domains": true, "citations": true,
		"allowed_callers": true, "cache_control": true, "strict": true,
		"use_cache": true, "response_inclusion": true,
	}
	for name := range tool {
		if !allowed[name] {
			return fmt.Errorf("web_fetch.%s is not supported", name)
		}
	}
	var name string
	if json.Unmarshal(tool["name"], &name) != nil || name != "web_fetch" {
		return errors.New("web_fetch.name must be web_fetch")
	}
	if err := requiredBoundedInteger(tool, "max_uses", 1, anthropicWebFetchMaxUses, &result.maxUses); err != nil {
		return err
	}
	if err := requiredBoundedInteger(tool, "max_content_tokens", 1, anthropicWebFetchMaxContentTokens, &result.maxContentTokens); err != nil {
		return err
	}
	allowedRaw, allowedPresent := tool["allowed_domains"]
	blockedRaw, blockedPresent := tool["blocked_domains"]
	allowedPresent = allowedPresent && !bytes.Equal(bytes.TrimSpace(allowedRaw), []byte("null"))
	blockedPresent = blockedPresent && !bytes.Equal(bytes.TrimSpace(blockedRaw), []byte("null"))
	if allowedPresent && blockedPresent {
		return errors.New("web_fetch.allowed_domains and web_fetch.blocked_domains cannot be combined")
	}
	for _, field := range []string{"allowed_domains", "blocked_domains"} {
		raw, present := tool[field]
		if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var domains []string
		if json.Unmarshal(raw, &domains) != nil || len(domains) > anthropicWebFetchDomainLimit {
			return fmt.Errorf("web_fetch.%s must contain at most %d domains", field, anthropicWebFetchDomainLimit)
		}
		for _, domain := range domains {
			if !validWebSearchDomain(domain) {
				return fmt.Errorf("web_fetch.%s contains an invalid domain", field)
			}
		}
	}
	if raw, present := tool["citations"]; present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var citations map[string]json.RawMessage
		if json.Unmarshal(raw, &citations) != nil || len(citations) != 1 {
			return errors.New("web_fetch.citations must contain only enabled")
		}
		var enabled bool
		if json.Unmarshal(citations["enabled"], &enabled) != nil {
			return errors.New("web_fetch.citations.enabled must be a boolean")
		}
	}
	if err := validateAnthropicWebToolStrict(tool["strict"], "web_fetch"); err != nil {
		return err
	}
	if raw := tool["use_cache"]; len(raw) > 0 {
		var value bool
		if !cacheBypassVersion {
			return errors.New("web_fetch.use_cache is not supported by this tool version")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return errors.New("web_fetch.use_cache must be a boolean")
		}
	}
	if err := validateAnthropicWebResponseInclusion(tool["response_inclusion"], "web_fetch", responseInclusionVersion); err != nil {
		return err
	}
	dynamic, err := validateAnthropicWebToolCallers(tool["allowed_callers"], "web_fetch", dynamicVersion)
	if err != nil {
		return err
	}
	result.dynamic = dynamic
	return nil
}

func anthropicWebFetchTargetEligibility(target providers.Target, request anthropicWebFetchRequest) (bool, string) {
	if !providers.NativeTarget("anthropic", target.Adapter) || target.Adapter != "anthropic" || target.Preset != "anthropic" {
		return false, "web_fetch_native_required"
	}
	if !slices.Contains(target.Capabilities, "chat") || !slices.Contains(target.UpstreamCapabilities, "chat") || !slices.Contains(target.Capabilities, "web_fetch") || !slices.Contains(target.UpstreamCapabilities, "web_fetch") {
		return false, "unsupported_capability"
	}
	if request.dynamic && (!slices.Contains(target.Capabilities, "web_fetch_dynamic") || !slices.Contains(target.UpstreamCapabilities, "web_fetch_dynamic")) {
		return false, "unsupported_capability"
	}
	if request.cacheBypass && (!slices.Contains(target.Capabilities, "web_fetch_cache_bypass") || !slices.Contains(target.UpstreamCapabilities, "web_fetch_cache_bypass")) {
		return false, "unsupported_capability"
	}
	if request.responseInclusion && (!slices.Contains(target.Capabilities, "web_fetch_response_inclusion") || !slices.Contains(target.UpstreamCapabilities, "web_fetch_response_inclusion")) {
		return false, "unsupported_capability"
	}
	if target.RoutingStrategy == "lowest_cost" || target.FreeOnly {
		return false, "web_fetch_price_contract_unavailable"
	}
	return true, ""
}

func parseAnthropicWebFetchUsage(raw []byte, maximum int64, dynamic bool, companion string) (*int64, bool, bool) {
	return parseAnthropicServerToolUsage(raw, "web_fetch_requests", companion, maximum, dynamic)
}

func parseAnthropicWebFetchStream(raw []byte, maximum int64, dynamic bool, companion string) (*int64, error) {
	return parseAnthropicServerToolStream(raw, "web_fetch_requests", companion, maximum, dynamic)
}
