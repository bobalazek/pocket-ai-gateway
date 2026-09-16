package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func validateEmbedding(envelope map[string]json.RawMessage) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(envelope["input"]))
	decoder.UseNumber()
	var input any
	if decoder.Decode(&input) != nil {
		return 0, errors.New("input must be a non-empty string, string array, token array, or token-array collection")
	}
	var cardinality int64
	switch value := input.(type) {
	case string:
		if value == "" {
			return 0, errors.New("embedding input strings must not be empty")
		}
		cardinality = 1
	case []any:
		if len(value) == 0 {
			return 0, errors.New("embedding input arrays must not be empty")
		}
		switch value[0].(type) {
		case string:
			if len(value) > 2048 {
				return 0, errors.New("embedding input collections accept at most 2048 items")
			}
			for _, item := range value {
				text, ok := item.(string)
				if !ok || text == "" {
					return 0, errors.New("embedding string arrays must contain only non-empty strings")
				}
			}
			cardinality = int64(len(value))
		case json.Number:
			if !validEmbeddingTokenIDs(value) {
				return 0, errors.New("embedding token arrays must contain only non-negative integers")
			}
			cardinality = 1
		case []any:
			if len(value) > 2048 {
				return 0, errors.New("embedding input collections accept at most 2048 items")
			}
			for _, item := range value {
				tokens, ok := item.([]any)
				if !ok || len(tokens) == 0 || !validEmbeddingTokenIDs(tokens) {
					return 0, errors.New("embedding token-array collections must contain only non-empty integer arrays")
				}
			}
			cardinality = int64(len(value))
		default:
			return 0, errors.New("input must be a non-empty string, string array, token array, or token-array collection")
		}
	default:
		return 0, errors.New("input must be a non-empty string, string array, token array, or token-array collection")
	}
	if raw, exists := envelope["dimensions"]; exists {
		var dimensions int64
		if json.Unmarshal(raw, &dimensions) != nil || dimensions < 1 {
			return 0, errors.New("dimensions must be a positive integer")
		}
	}
	if raw, exists := envelope["encoding_format"]; exists {
		var format string
		if json.Unmarshal(raw, &format) != nil || format != "float" && format != "base64" {
			return 0, errors.New("encoding_format must be float or base64")
		}
	}
	if raw, exists := envelope["user"]; exists {
		var user string
		if json.Unmarshal(raw, &user) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return 0, errors.New("user must be a string")
		}
	}
	return cardinality, nil
}

func validEmbeddingTokenIDs(values []any) bool {
	for _, value := range values {
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || parsed < 0 {
			return false
		}
	}
	return true
}

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

type imageGenerationRequest struct {
	stream        bool
	partialImages int64
}

func validateImageGeneration(envelope map[string]json.RawMessage) (imageGenerationRequest, error) {
	if err := validateImageJSON(envelope); err != nil {
		return imageGenerationRequest{}, err
	}
	stream, err := jsonBoolean(envelope, "stream", false)
	if err != nil {
		return imageGenerationRequest{}, err
	}
	n := int64(1)
	if raw := bytes.TrimSpace(envelope["n"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		_ = json.Unmarshal(raw, &n)
	}
	partialImages := int64(0)
	if raw := bytes.TrimSpace(envelope["partial_images"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if json.Unmarshal(raw, &partialImages) != nil || partialImages < 0 || partialImages > 3 {
			return imageGenerationRequest{}, errors.New("partial_images must be an integer between 0 and 3")
		}
		if !stream {
			return imageGenerationRequest{}, errors.New("partial_images requires streaming image generation")
		}
	}
	if stream && n != 1 {
		return imageGenerationRequest{}, errors.New("streaming image generation requires n=1")
	}
	return imageGenerationRequest{stream: stream, partialImages: partialImages}, nil
}

func validateImageEditBatch(envelope map[string]json.RawMessage) error {
	if err := validateImageJSON(envelope); err != nil {
		return err
	}
	if stream, err := jsonBoolean(envelope, "stream", false); err != nil || stream {
		return errors.New("stream must be false or omitted in Batches")
	}
	if raw := bytes.TrimSpace(envelope["partial_images"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return errors.New("partial_images is not supported in Batches")
	}
	var images []json.RawMessage
	if json.Unmarshal(envelope["images"], &images) != nil || len(images) < 1 || len(images) > 16 {
		return errors.New("images must contain 1-16 image_url references")
	}
	for _, image := range images {
		if err := validateBatchImageReference(image); err != nil {
			return err
		}
	}
	if raw := bytes.TrimSpace(envelope["mask"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := validateBatchImageReference(raw); err != nil {
			return fmt.Errorf("mask: %w", err)
		}
	}
	return nil
}

func validateImageJSON(envelope map[string]json.RawMessage) error {
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
	return nil
}

func validateBatchImageReference(raw json.RawMessage) error {
	var reference map[string]json.RawMessage
	if json.Unmarshal(raw, &reference) != nil || len(reference) != 1 {
		return errors.New("each image reference must contain only image_url")
	}
	if _, exists := reference["file_id"]; exists {
		return errors.New("file_id image references are not supported")
	}
	var value string
	if json.Unmarshal(reference["image_url"], &value) != nil || value == "" || len(value) > 20_971_520 {
		return errors.New("image_url must be a bounded HTTPS or base64 image URL")
	}
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,"} {
		if strings.HasPrefix(value, prefix) {
			decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(strings.TrimPrefix(value, prefix)))
			size, err := io.Copy(io.Discard, io.LimitReader(decoder, maxInferenceBody+1))
			if err != nil || size < 1 || size > maxInferenceBody {
				return errors.New("image_url contains invalid or oversized base64 image data")
			}
			return nil
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("image_url must be a bounded HTTPS or base64 image URL")
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
			if kind, _ := tool["type"].(string); kind != "function" && kind != "web_search" && kind != "file_search" {
				return false, errors.New("only function, web_search, and file_search tools are supported by Responses")
			}
		}
	}
	if _, err := validateResponseWebSearch(envelope); err != nil {
		return false, err
	}
	if _, err := validateResponseFileSearch(envelope); err != nil {
		return false, err
	}
	return stored, nil
}
