package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

func validateModeration(envelope map[string]json.RawMessage) error {
	if _, exists := envelope["stream"]; exists {
		return errors.New("stream is not supported for moderations")
	}
	var input any
	if raw, exists := envelope["input"]; !exists || json.Unmarshal(raw, &input) != nil {
		return errors.New("input must be a string or non-empty array")
	}
	switch value := input.(type) {
	case string:
		return nil
	case []any:
		if len(value) == 0 {
			break
		}
		mode := ""
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				if mode == "objects" {
					return errors.New("input array must contain only strings or only multimodal objects")
				}
				mode = "strings"
			case map[string]any:
				if mode == "strings" || !validModerationObject(typed) {
					return errors.New("input array must contain only strings or valid multimodal objects")
				}
				mode = "objects"
			default:
				return errors.New("input array must contain only strings or valid multimodal objects")
			}
		}
		return nil
	}
	return errors.New("input must be a string or non-empty array")
}

func validModerationObject(input map[string]any) bool {
	switch input["type"] {
	case "text":
		_, ok := input["text"].(string)
		return ok
	case "image_url":
		image, ok := input["image_url"].(map[string]any)
		url, valid := image["url"].(string)
		return ok && valid && url != ""
	default:
		return false
	}
}

func rewriteResponseModel(raw []byte, publicID string) ([]byte, error) {
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || response == nil {
		return nil, errors.New("provider returned an invalid response")
	}
	response["model"], _ = json.Marshal(publicID)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(response) != nil || encoded.Len() > maxInferenceBody+1 {
		return nil, errors.New("provider response exceeds 16 MiB")
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

func validateResponseInputTokens(raw []byte) (int64, error) {
	var response struct {
		Object      string          `json:"object"`
		InputTokens json.RawMessage `json:"input_tokens"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Object != "response.input_tokens" {
		return 0, errors.New("provider returned an invalid input-token response")
	}
	var count *int64
	if json.Unmarshal(response.InputTokens, &count) != nil || count == nil || *count < 0 || *count > 9_007_199_254_740_991 {
		return 0, errors.New("provider returned an invalid input-token count")
	}
	return *count, nil
}

func validateImageGeneration(envelope map[string]json.RawMessage) error {
	var prompt string
	if json.Unmarshal(envelope["prompt"], &prompt) != nil || strings.TrimSpace(prompt) == "" || len([]rune(prompt)) > 32_000 {
		return errors.New("prompt must contain 1-32000 characters")
	}
	for name, bounds := range map[string][2]int64{"n": {1, 10}, "output_compression": {0, 100}} {
		raw := bytes.TrimSpace(envelope[name])
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < bounds[0] || value > bounds[1] {
			return fmt.Errorf("%s must be an integer between %d and %d", name, bounds[0], bounds[1])
		}
	}
	if raw := bytes.TrimSpace(envelope["stream"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("false")) {
		return errors.New("streaming image generation is not supported")
	}
	if raw := bytes.TrimSpace(envelope["partial_images"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return errors.New("partial_images requires streaming image generation")
	}
	return nil
}

func validateSpeech(envelope map[string]json.RawMessage) error {
	var input string
	if json.Unmarshal(envelope["input"], &input) != nil || strings.TrimSpace(input) == "" || len([]rune(input)) > 4096 {
		return errors.New("input must contain 1-4096 characters")
	}
	var voice string
	if json.Unmarshal(envelope["voice"], &voice) != nil || !slices.Contains([]string{"alloy", "ash", "ballad", "coral", "echo", "fable", "onyx", "nova", "sage", "shimmer", "verse", "marin", "cedar"}, voice) {
		return errors.New("voice must be a built-in OpenAI voice; custom voice references are not supported")
	}
	if raw := bytes.TrimSpace(envelope["response_format"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var format string
		if json.Unmarshal(raw, &format) != nil || !slices.Contains([]string{"mp3", "opus", "aac", "flac", "wav", "pcm"}, format) {
			return errors.New("response_format must be mp3, opus, aac, flac, wav, or pcm")
		}
	}
	if raw := bytes.TrimSpace(envelope["stream_format"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var format string
		if json.Unmarshal(raw, &format) != nil || format != "audio" {
			return errors.New("stream_format must be audio; SSE speech is not supported")
		}
	}
	if raw := bytes.TrimSpace(envelope["stream"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("false")) {
		return errors.New("streaming speech responses are not supported")
	}
	if raw := bytes.TrimSpace(envelope["speed"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var speed float64
		if json.Unmarshal(raw, &speed) != nil || speed < 0.25 || speed > 4 {
			return errors.New("speed must be between 0.25 and 4")
		}
	}
	if raw := bytes.TrimSpace(envelope["instructions"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var instructions string
		if json.Unmarshal(raw, &instructions) != nil {
			return errors.New("instructions must be a string")
		}
	}
	return nil
}

func validateResponses(envelope map[string]json.RawMessage) (bool, error) {
	stored := true
	if raw, exists := envelope["store"]; exists && json.Unmarshal(raw, &stored) != nil {
		return false, errors.New("store must be a boolean")
	}
	var stream bool
	if raw, exists := envelope["stream"]; exists && json.Unmarshal(raw, &stream) != nil {
		return false, errors.New("stream must be a boolean")
	}
	if stored && stream {
		return false, errors.New("stored streaming Responses are not supported")
	}
	for _, field := range []string{"background", "conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("false")) && !bytes.Equal(raw, []byte(`""`)) {
			return false, errors.New(field + " is not supported by Responses")
		}
	}
	if raw := envelope["tools"]; len(raw) > 0 {
		var tools []map[string]any
		if json.Unmarshal(raw, &tools) != nil {
			return false, errors.New("tools must be an array")
		}
		for _, tool := range tools {
			if kind, _ := tool["type"].(string); kind != "function" && kind != "web_search" {
				return false, errors.New("only function and web_search tools are supported by Responses")
			}
		}
	}
	if _, err := validateResponseWebSearch(envelope); err != nil {
		return false, err
	}
	return stored, nil
}
