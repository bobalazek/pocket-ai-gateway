package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

const anthropicWebSearchDomainMaxSize = 1024

const iso3166Alpha2Codes = "|AD|AE|AF|AG|AI|AL|AM|AO|AQ|AR|AS|AT|AU|AW|AX|AZ|BA|BB|BD|BE|BF|BG|BH|BI|BJ|BL|BM|BN|BO|BQ|BR|BS|BT|BV|BW|BY|BZ|CA|CC|CD|CF|CG|CH|CI|CK|CL|CM|CN|CO|CR|CU|CV|CW|CX|CY|CZ|DE|DJ|DK|DM|DO|DZ|EC|EE|EG|EH|ER|ES|ET|FI|FJ|FK|FM|FO|FR|GA|GB|GD|GE|GF|GG|GH|GI|GL|GM|GN|GP|GQ|GR|GS|GT|GU|GW|GY|HK|HM|HN|HR|HT|HU|ID|IE|IL|IM|IN|IO|IQ|IR|IS|IT|JE|JM|JO|JP|KE|KG|KH|KI|KM|KN|KP|KR|KW|KY|KZ|LA|LB|LC|LI|LK|LR|LS|LT|LU|LV|LY|MA|MC|MD|ME|MF|MG|MH|MK|ML|MM|MN|MO|MP|MQ|MR|MS|MT|MU|MV|MW|MX|MY|MZ|NA|NC|NE|NF|NG|NI|NL|NO|NP|NR|NU|NZ|OM|PA|PE|PF|PG|PH|PK|PL|PM|PN|PR|PS|PT|PW|PY|QA|RE|RO|RS|RU|RW|SA|SB|SC|SD|SE|SG|SH|SI|SJ|SK|SL|SM|SN|SO|SR|SS|ST|SV|SX|SY|SZ|TC|TD|TF|TG|TH|TJ|TK|TL|TM|TN|TO|TR|TT|TV|TW|TZ|UA|UG|UM|US|UY|UZ|VA|VC|VE|VG|VI|VN|VU|WF|WS|YE|YT|ZA|ZM|ZW|"

type anthropicWebSearchRequest struct {
	enabled bool
	dynamic bool
	maxUses int64
}

func validateAnthropicWebSearch(envelope map[string]json.RawMessage) (anthropicWebSearchRequest, error) {
	var result anthropicWebSearchRequest
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
		case "web_search_20250305", "web_search_20260209":
			if result.enabled {
				return result, errors.New("at most one web_search tool is supported")
			}
			var err error
			result.dynamic, err = validateAnthropicWebSearchTool(tool, &result.maxUses, kind == "web_search_20260209")
			if err != nil {
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

func validateAnthropicWebSearchTool(tool map[string]json.RawMessage, maxUses *int64, dynamicVersion bool) (bool, error) {
	allowed := map[string]bool{
		"type": true, "name": true, "max_uses": true, "allowed_domains": true,
		"blocked_domains": true, "user_location": true, "allowed_callers": true,
		"cache_control": true, "strict": true,
	}
	for name := range tool {
		if !allowed[name] {
			return false, fmt.Errorf("web_search.%s is not supported", name)
		}
	}
	var name string
	if json.Unmarshal(tool["name"], &name) != nil || name != "web_search" {
		return false, errors.New("web_search.name must be web_search")
	}
	if err := requiredBoundedInteger(tool, "max_uses", 1, webSearchMaxCalls, maxUses); err != nil {
		return false, err
	}
	allowedRaw, allowedPresent := tool["allowed_domains"]
	blockedRaw, blockedPresent := tool["blocked_domains"]
	allowedPresent = allowedPresent && !bytes.Equal(bytes.TrimSpace(allowedRaw), []byte("null"))
	blockedPresent = blockedPresent && !bytes.Equal(bytes.TrimSpace(blockedRaw), []byte("null"))
	if allowedPresent && blockedPresent {
		return false, errors.New("web_search.allowed_domains and web_search.blocked_domains cannot be combined")
	}
	for _, field := range []string{"allowed_domains", "blocked_domains"} {
		raw, present := tool[field]
		if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var domains []string
		if json.Unmarshal(raw, &domains) != nil || len(domains) > webSearchDomainLimit {
			return false, fmt.Errorf("web_search.%s must contain at most 100 domains", field)
		}
		for _, domain := range domains {
			if !validAnthropicWebSearchDomain(domain) {
				return false, fmt.Errorf("web_search.%s contains an invalid domain", field)
			}
		}
	}
	if err := validateAnthropicWebSearchLocation(tool["user_location"]); err != nil {
		return false, err
	}
	if err := validateAnthropicWebToolStrict(tool["strict"], "web_search"); err != nil {
		return false, err
	}
	return validateAnthropicWebToolCallers(tool["allowed_callers"], "web_search", dynamicVersion)
}

func validateAnthropicWebToolStrict(raw json.RawMessage, tool string) error {
	if len(raw) == 0 {
		return nil
	}
	var strict bool
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &strict) != nil {
		return fmt.Errorf("%s.strict must be a boolean", tool)
	}
	return nil
}

func validateAnthropicWebToolCallers(raw json.RawMessage, tool string, dynamicVersion bool) (bool, error) {
	if len(raw) == 0 {
		return dynamicVersion, nil
	}
	var callers []string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &callers) != nil || len(callers) == 0 {
		return false, fmt.Errorf("%s.allowed_callers must be a non-empty caller list", tool)
	}
	seen := make(map[string]bool, len(callers))
	dynamic := false
	for _, caller := range callers {
		if seen[caller] {
			return false, fmt.Errorf("%s.allowed_callers must not contain duplicates", tool)
		}
		seen[caller] = true
		switch caller {
		case "direct":
		case "code_execution_20250825", "code_execution_20260120", "code_execution_20260521":
			dynamic = true
		default:
			return false, fmt.Errorf("%s.allowed_callers contains an unsupported caller", tool)
		}
	}
	return dynamic, nil
}

func validateAnthropicWebSearchLocation(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var location map[string]json.RawMessage
	if json.Unmarshal(trimmed, &location) != nil || location == nil {
		return errors.New("web_search.user_location must be an approximate location object")
	}
	allowed := map[string]bool{"type": true, "city": true, "country": true, "region": true, "timezone": true}
	localized := false
	for field, rawValue := range location {
		if !allowed[field] {
			return fmt.Errorf("web_search.user_location.%s is not supported", field)
		}
		if field == "type" {
			continue
		}
		var value string
		if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
			continue
		}
		if json.Unmarshal(rawValue, &value) != nil || value == "" || utf8.RuneCountInString(value) > 255 {
			return fmt.Errorf("web_search.user_location.%s must be a string of at most 255 characters", field)
		}
		if field == "country" && !strings.Contains(iso3166Alpha2Codes, "|"+strings.ToUpper(value)+"|") {
			return errors.New("web_search.user_location.country must be an ISO 3166-1 alpha-2 country code")
		}
		if field == "timezone" {
			if value == "Local" {
				return errors.New("web_search.user_location.timezone must be a valid IANA timezone")
			}
			if _, err := time.LoadLocation(value); err != nil {
				return errors.New("web_search.user_location.timezone must be a valid IANA timezone")
			}
		}
		localized = true
	}
	var kind string
	if json.Unmarshal(location["type"], &kind) != nil || kind != "approximate" {
		return errors.New("web_search.user_location.type must be approximate")
	}
	if !localized {
		return errors.New("web_search.user_location must include city, country, region, or timezone")
	}
	return nil
}

func validAnthropicWebSearchDomain(value string) bool {
	if value == "" || len(value) > anthropicWebSearchDomainMaxSize || strings.TrimSpace(value) != value || strings.ContainsAny(value, "?#\\") {
		return false
	}
	host, path, _ := strings.Cut(value, "/")
	if !validWebSearchDomain(host) {
		return false
	}
	for _, character := range path {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func anthropicWebSearchTargetEligibility(target providers.Target, dynamic bool) (bool, string) {
	if !providers.NativeTarget("anthropic", target.Adapter) || target.Adapter != "anthropic" || target.Preset != "anthropic" {
		return false, "web_search_native_required"
	}
	if !slices.Contains(target.Capabilities, "chat") || !slices.Contains(target.UpstreamCapabilities, "chat") || !slices.Contains(target.Capabilities, "web_search") || !slices.Contains(target.UpstreamCapabilities, "web_search") {
		return false, "unsupported_capability"
	}
	if dynamic && (!slices.Contains(target.Capabilities, "web_search_dynamic") || !slices.Contains(target.UpstreamCapabilities, "web_search_dynamic")) {
		return false, "unsupported_capability"
	}
	if target.RoutingStrategy == "lowest_cost" || target.FreeOnly {
		return false, "web_search_price_contract_unavailable"
	}
	return true, ""
}

func parseAnthropicWebSearchUsage(raw []byte, maximum int64, dynamic bool) (*int64, bool, bool) {
	return parseAnthropicServerToolUsage(raw, "web_search_requests", maximum, dynamic)
}

func parseAnthropicServerToolUsage(raw []byte, field string, maximum int64, allowCodeExecution bool) (*int64, bool, bool) {
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || response == nil {
		return nil, false, false
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(response["usage"], &usage) != nil || usage == nil {
		return nil, false, false
	}
	rawServerToolUse, exists := usage["server_tool_use"]
	if !exists {
		return nil, false, false
	}
	if bytes.Equal(bytes.TrimSpace(rawServerToolUse), []byte("null")) {
		zero := int64(0)
		return &zero, true, false
	}
	var serverToolUse map[string]json.RawMessage
	if json.Unmarshal(rawServerToolUse, &serverToolUse) != nil || serverToolUse == nil {
		return nil, false, false
	}
	var count int64
	rawCount, exists := serverToolUse[field]
	if !exists || bytes.Equal(bytes.TrimSpace(rawCount), []byte("null")) || json.Unmarshal(rawCount, &count) != nil || count < 0 {
		return nil, false, false
	}
	for name, raw := range serverToolUse {
		if name == field || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var other int64
		if json.Unmarshal(raw, &other) != nil || other < 0 || other != 0 && !(allowCodeExecution && name == "code_execution_requests") {
			return nil, false, false
		}
	}
	if count > maximum {
		return nil, false, true
	}
	return &count, true, false
}

func parseAnthropicWebSearchStream(raw []byte, maximum int64, dynamic bool) (*int64, error) {
	return parseAnthropicServerToolStream(raw, "web_search_requests", maximum, dynamic)
}

func parseAnthropicServerToolStream(raw []byte, field string, maximum int64, allowCodeExecution bool) (*int64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxInferenceBody+1)
	var data bytes.Buffer
	var eventName string
	var count *int64
	started, stopped, frames := false, false, 0
	flush := func() error {
		defer func() {
			data.Reset()
			eventName = ""
		}()
		object := bytes.TrimSpace(data.Bytes())
		if len(object) == 0 {
			return nil
		}
		frames++
		if frames > maxConversationStreamFrames {
			return errors.New("provider returned too many SSE events")
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(object, &event) != nil || event == nil {
			return errors.New("provider returned malformed SSE data")
		}
		var kind string
		if json.Unmarshal(event["type"], &kind) != nil || kind == "" {
			return errors.New("provider returned an untyped SSE event")
		}
		if eventName != "" && eventName != kind {
			return errors.New("provider returned mismatched SSE event and data types")
		}
		if stopped {
			return errors.New("provider returned data after message_stop")
		}
		switch kind {
		case "error":
			return errors.New("provider returned an in-stream error")
		case "ping":
			return nil
		case "message_start":
			if started {
				return errors.New("provider returned duplicate message_start")
			}
			started = true
		case "message_delta":
			if !started {
				return errors.New("provider omitted message_start")
			}
			var delta struct {
				Usage struct {
					OutputTokens *int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(object, &delta) != nil || delta.Usage.OutputTokens == nil || *delta.Usage.OutputTokens < 0 {
				return errors.New("provider omitted terminal output token usage")
			}
			parsed, known, exceeded := parseAnthropicServerToolUsage(object, field, maximum, allowCodeExecution)
			if exceeded {
				return errors.New("provider exceeded max_uses")
			}
			if !known {
				return errors.New("provider omitted terminal server-tool usage")
			}
			if count != nil && *parsed < *count {
				return errors.New("provider decreased cumulative server-tool usage")
			}
			count = parsed
		case "message_stop":
			if !started {
				return errors.New("provider omitted message_start")
			}
			if count == nil {
				return errors.New("provider omitted terminal server-tool usage")
			}
			stopped = true
		default:
			if !started {
				return errors.New("provider omitted message_start")
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			eventName = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:"))))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("provider returned malformed SSE data")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if !stopped {
		return nil, errors.New("provider stream ended before message_stop")
	}
	tokens := parseUsageDetails("anthropic", raw)
	if tokens.inputTokens == nil || tokens.outputTokens == nil {
		return nil, errors.New("provider omitted terminal token usage")
	}
	return count, nil
}
