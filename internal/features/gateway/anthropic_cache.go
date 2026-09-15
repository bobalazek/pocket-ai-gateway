package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
)

type promptCacheRequest struct {
	enabled bool
}

func validatePromptCache(envelope map[string]json.RawMessage) (promptCacheRequest, error) {
	topLevelTTL, topLevel, err := cacheControlTTL(envelope["cache_control"])
	if err != nil {
		return promptCacheRequest{}, err
	}
	explicit := make([]string, 0, 4)
	type cacheableBlock struct {
		ttl        string
		controlled bool
	}
	var lastCacheable *cacheableBlock
	collect := func(raw json.RawMessage, tools bool) error {
		var text string
		if !tools && json.Unmarshal(raw, &text) == nil {
			if text != "" {
				lastCacheable = &cacheableBlock{}
			}
			return nil
		}
		var blocks []map[string]json.RawMessage
		if len(raw) == 0 || json.Unmarshal(raw, &blocks) != nil {
			return nil
		}
		for _, block := range blocks {
			ttl, present, err := cacheControlTTL(block["cache_control"])
			if err != nil {
				return err
			}
			cacheable := tools || anthropicContentBlockCacheable(block)
			if present && !cacheable {
				return errors.New("cache_control cannot be used on a non-cacheable content block")
			}
			if present {
				explicit = append(explicit, ttl)
			}
			if cacheable {
				lastCacheable = &cacheableBlock{ttl: ttl, controlled: present}
			}
		}
		return nil
	}
	if err := collect(envelope["tools"], true); err != nil {
		return promptCacheRequest{}, err
	}
	if err := collect(envelope["system"], false); err != nil {
		return promptCacheRequest{}, err
	}
	var messages []map[string]json.RawMessage
	if len(envelope["messages"]) > 0 && json.Unmarshal(envelope["messages"], &messages) == nil {
		for _, message := range messages {
			if err := collect(message["content"], false); err != nil {
				return promptCacheRequest{}, err
			}
		}
	}
	seenFiveMinute := false
	for _, ttl := range explicit {
		if ttl == "5m" {
			seenFiveMinute = true
		} else if seenFiveMinute {
			return promptCacheRequest{}, errors.New("1h cache breakpoints must precede 5m cache breakpoints")
		}
	}
	slots := len(explicit)
	if topLevel && lastCacheable != nil {
		if lastCacheable.controlled {
			if lastCacheable.ttl != topLevelTTL {
				return promptCacheRequest{}, errors.New("top-level cache_control ttl must match the last cacheable block")
			}
		} else {
			slots++
			if topLevelTTL == "1h" && seenFiveMinute {
				return promptCacheRequest{}, errors.New("1h cache breakpoints must precede 5m cache breakpoints")
			}
		}
	}
	if slots > 4 {
		return promptCacheRequest{}, errors.New("prompt caching supports at most four cache breakpoints")
	}
	return promptCacheRequest{enabled: topLevel || len(explicit) > 0}, nil
}

func anthropicContentBlockCacheable(block map[string]json.RawMessage) bool {
	var kind string
	_ = json.Unmarshal(block["type"], &kind)
	switch kind {
	case "thinking", "redacted_thinking", "citation", "citations", "char_location", "page_location", "content_block_location", "web_search_result_location", "search_result_location":
		return false
	case "text":
		var text string
		return json.Unmarshal(block["text"], &text) == nil && text != ""
	default:
		return true
	}
}

func cacheControlTTL(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || len(value) < 1 || len(value) > 2 {
		return "", false, errors.New("cache_control must be null or an ephemeral cache control")
	}
	for key := range value {
		if key != "type" && key != "ttl" {
			return "", false, errors.New("cache_control supports only type and ttl")
		}
	}
	var kind string
	if json.Unmarshal(value["type"], &kind) != nil || kind != "ephemeral" {
		return "", false, errors.New("cache_control.type must be ephemeral")
	}
	ttl := "5m"
	if ttlRaw, ok := value["ttl"]; ok {
		var explicitTTL string
		if json.Unmarshal(ttlRaw, &explicitTTL) != nil || explicitTTL != "5m" && explicitTTL != "1h" {
			return "", false, errors.New("cache_control.ttl must be 5m or 1h")
		}
		ttl = explicitTTL
	}
	return ttl, true, nil
}
