package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

const maxOpenAICompletionBytes = 16 << 20

var ErrInvalidOpenAICompletion = errors.New("invalid OpenAI Completion response")

type OpenAICompletionRequest struct {
	PromptCount int64
	Candidates  int64
	Stream      bool
}

func ValidateOpenAICompletion(envelope map[string]json.RawMessage) (OpenAICompletionRequest, error) {
	allowed := map[string]bool{
		"model": true, "prompt": true, "best_of": true, "echo": true, "frequency_penalty": true,
		"logit_bias": true, "logprobs": true, "max_tokens": true, "n": true, "presence_penalty": true,
		"seed": true, "stop": true, "stream": true, "stream_options": true, "suffix": true,
		"temperature": true, "top_p": true, "user": true,
	}
	for name := range envelope {
		if !allowed[name] {
			return OpenAICompletionRequest{}, fmt.Errorf("%s is not supported by legacy Completions", name)
		}
	}
	if _, exists := envelope["prompt"]; !exists {
		return OpenAICompletionRequest{}, errors.New("prompt is required")
	}
	promptCount, err := completionPromptCount(envelope["prompt"])
	if err != nil {
		return OpenAICompletionRequest{}, err
	}
	n, err := completionInteger(envelope, "n", 1, 128, 1)
	if err != nil {
		return OpenAICompletionRequest{}, err
	}
	bestOf, err := completionInteger(envelope, "best_of", 0, 20, 0)
	if err != nil {
		return OpenAICompletionRequest{}, err
	}
	if bestOf > 0 && bestOf <= n {
		return OpenAICompletionRequest{}, errors.New("best_of must be greater than n")
	}
	stream, err := completionBoolean(envelope, "stream")
	if err != nil {
		return OpenAICompletionRequest{}, err
	}
	if stream && bestOf > 0 {
		return OpenAICompletionRequest{}, errors.New("best_of is not supported with streaming")
	}
	if raw, exists := envelope["stream_options"]; exists && !jsonNull(raw) {
		if !stream {
			return OpenAICompletionRequest{}, errors.New("stream_options requires stream=true")
		}
		var options map[string]json.RawMessage
		if json.Unmarshal(raw, &options) != nil || options == nil {
			return OpenAICompletionRequest{}, errors.New("stream_options must be an object")
		}
		for name, value := range options {
			if name != "include_usage" && name != "include_obfuscation" {
				return OpenAICompletionRequest{}, fmt.Errorf("stream_options.%s is unsupported", name)
			}
			var enabled bool
			if json.Unmarshal(value, &enabled) != nil {
				return OpenAICompletionRequest{}, fmt.Errorf("stream_options.%s must be a boolean", name)
			}
		}
	}
	for name, bounds := range map[string][2]float64{
		"frequency_penalty": {-2, 2}, "presence_penalty": {-2, 2}, "temperature": {0, 2}, "top_p": {0, 1},
	} {
		if err := completionNumber(envelope, name, bounds[0], bounds[1]); err != nil {
			return OpenAICompletionRequest{}, err
		}
	}
	if _, err := completionInteger(envelope, "logprobs", 0, 5, 0); err != nil {
		return OpenAICompletionRequest{}, err
	}
	if _, err := completionInteger(envelope, "max_tokens", 0, 9_007_199_254_740_991, 0); err != nil {
		return OpenAICompletionRequest{}, err
	}
	if raw, exists := envelope["seed"]; exists && !jsonNull(raw) {
		var seed int64
		if json.Unmarshal(raw, &seed) != nil {
			return OpenAICompletionRequest{}, errors.New("seed must be an integer or null")
		}
	}
	if _, err := completionBoolean(envelope, "echo"); err != nil {
		return OpenAICompletionRequest{}, err
	}
	for _, name := range []string{"suffix", "user"} {
		if raw, exists := envelope[name]; exists && !jsonNull(raw) {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return OpenAICompletionRequest{}, fmt.Errorf("%s must be a string or null", name)
			}
		}
	}
	if err := validateCompletionStop(envelope["stop"]); err != nil {
		return OpenAICompletionRequest{}, err
	}
	if err := validateCompletionLogitBias(envelope["logit_bias"]); err != nil {
		return OpenAICompletionRequest{}, err
	}
	candidates := max(n, bestOf)
	if candidates == 0 {
		candidates = 1
	}
	if promptCount > math.MaxInt64/candidates {
		return OpenAICompletionRequest{}, errors.New("prompt and candidate count exceed the supported range")
	}
	return OpenAICompletionRequest{PromptCount: promptCount, Candidates: candidates, Stream: stream}, nil
}

func completionPromptCount(raw json.RawMessage) (int64, error) {
	if jsonNull(raw) {
		return 1, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var prompt any
	if decoder.Decode(&prompt) != nil {
		return 0, errors.New("prompt must be null, a string, a string array, a token array, or a token-array collection")
	}
	switch value := prompt.(type) {
	case string:
		return 1, nil
	case []any:
		if len(value) == 0 {
			return 0, errors.New("prompt arrays must not be empty")
		}
		switch value[0].(type) {
		case string:
			for _, item := range value {
				if _, ok := item.(string); !ok {
					return 0, errors.New("prompt string arrays must contain only strings")
				}
			}
			return int64(len(value)), nil
		case json.Number:
			if !validCompletionTokenIDs(value) {
				return 0, errors.New("prompt token arrays must contain only non-negative integers")
			}
			return 1, nil
		case []any:
			for _, item := range value {
				tokens, ok := item.([]any)
				if !ok || len(tokens) == 0 || !validCompletionTokenIDs(tokens) {
					return 0, errors.New("prompt token-array collections must contain only non-empty integer arrays")
				}
			}
			return int64(len(value)), nil
		}
	}
	return 0, errors.New("prompt must be null, a string, a string array, a token array, or a token-array collection")
}

func validCompletionTokenIDs(values []any) bool {
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

func completionInteger(envelope map[string]json.RawMessage, name string, minimum, maximum, fallback int64) (int64, error) {
	raw, exists := envelope[name]
	if !exists || jsonNull(raw) {
		return fallback, nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func completionNumber(envelope map[string]json.RawMessage, name string, minimum, maximum float64) error {
	raw, exists := envelope[name]
	if !exists || jsonNull(raw) {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil || value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %v and %v", name, minimum, maximum)
	}
	return nil
}

func completionBoolean(envelope map[string]json.RawMessage, name string) (bool, error) {
	raw, exists := envelope[name]
	if !exists || jsonNull(raw) {
		return false, nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, fmt.Errorf("%s must be a boolean or null", name)
	}
	return value, nil
}

func validateCompletionStop(raw json.RawMessage) error {
	if len(raw) == 0 || jsonNull(raw) {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return nil
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) > 4 {
		return errors.New("stop must be null, a string, or an array of at most 4 strings")
	}
	return nil
}

func validateCompletionLogitBias(raw json.RawMessage) error {
	if len(raw) == 0 || jsonNull(raw) {
		return nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return errors.New("logit_bias must be an object or null")
	}
	for token, rawValue := range values {
		parsed, err := strconv.ParseInt(token, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("logit_bias keys must be token IDs")
		}
		var value float64
		if json.Unmarshal(rawValue, &value) != nil || value < -100 || value > 100 {
			return errors.New("logit_bias values must be between -100 and 100")
		}
	}
	return nil
}

func jsonNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func NormalizeOpenAICompletionResponse(raw []byte, publicModel string) ([]byte, error) {
	return normalizeOpenAICompletionResponse(raw, publicModel, false)
}

func normalizeOpenAICompletionResponse(raw []byte, publicModel string, allowPartial bool) ([]byte, error) {
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || response == nil {
		return nil, fmt.Errorf("%w: provider returned invalid JSON", ErrInvalidOpenAICompletion)
	}
	if err := validateOpenAICompletionResponse(response, allowPartial); err != nil {
		return nil, err
	}
	response["model"], _ = json.Marshal(publicModel)
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxOpenAICompletionBytes {
		return nil, fmt.Errorf("%w: provider response exceeds 16 MiB", ErrInvalidOpenAICompletion)
	}
	return encoded, nil
}

func validateOpenAICompletionResponse(response map[string]json.RawMessage, allowPartial bool) error {
	var id, model, object string
	var created int64
	if raw, exists := response["id"]; !exists || jsonNull(raw) || json.Unmarshal(raw, &id) != nil || id == "" {
		return fmt.Errorf("%w: provider omitted id", ErrInvalidOpenAICompletion)
	}
	if raw, exists := response["created"]; !exists || jsonNull(raw) || json.Unmarshal(raw, &created) != nil || created < 0 {
		return fmt.Errorf("%w: provider omitted created", ErrInvalidOpenAICompletion)
	}
	if raw, exists := response["model"]; !exists || jsonNull(raw) || json.Unmarshal(raw, &model) != nil || model == "" {
		return fmt.Errorf("%w: provider omitted model", ErrInvalidOpenAICompletion)
	}
	if json.Unmarshal(response["object"], &object) != nil || object != "text_completion" {
		return fmt.Errorf("%w: provider returned an invalid object", ErrInvalidOpenAICompletion)
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(response["choices"], &choices) != nil || choices == nil {
		return fmt.Errorf("%w: provider omitted choices", ErrInvalidOpenAICompletion)
	}
	for _, choice := range choices {
		var text string
		var index int64
		if raw, exists := choice["text"]; !exists || jsonNull(raw) || json.Unmarshal(raw, &text) != nil {
			return fmt.Errorf("%w: provider returned invalid choice text", ErrInvalidOpenAICompletion)
		}
		if raw, exists := choice["index"]; !exists || jsonNull(raw) || json.Unmarshal(raw, &index) != nil || index < 0 {
			return fmt.Errorf("%w: provider returned invalid choice index", ErrInvalidOpenAICompletion)
		}
		if raw, exists := choice["logprobs"]; !exists || !jsonNull(raw) && !jsonObject(raw) {
			return fmt.Errorf("%w: provider returned invalid choice logprobs", ErrInvalidOpenAICompletion)
		}
		if raw, exists := choice["finish_reason"]; !exists || !completionFinishReason(raw, allowPartial) {
			return fmt.Errorf("%w: provider returned invalid choice finish_reason", ErrInvalidOpenAICompletion)
		}
	}
	return nil
}

func jsonObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func completionFinishReason(raw json.RawMessage, allowPartial bool) bool {
	if jsonNull(raw) {
		return allowPartial
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return value == "stop" || value == "length" || value == "content_filter"
}

func CopyOpenAICompletionStream(destination io.Writer, source io.Reader, publicModel string) error {
	reader := bufio.NewReader(source)
	var frame bytes.Buffer
	lineStart := 0
	done := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > maxOpenAICompletionBytes-frame.Len() {
			return fmt.Errorf("%w: provider event exceeds 16 MiB", ErrInvalidOpenAICompletion)
		}
		frame.Write(fragment)
		if readErr == nil {
			line := frame.Bytes()[lineStart:]
			lineStart = frame.Len()
			if !bytes.Equal(line, []byte("\n")) && !bytes.Equal(line, []byte("\r\n")) {
				continue
			}
			if done {
				return fmt.Errorf("%w: provider sent data after [DONE]", ErrInvalidOpenAICompletion)
			}
			terminal, err := writeOpenAICompletionStreamFrame(destination, frame.Bytes(), publicModel)
			if err != nil {
				return err
			}
			done = terminal
			frame.Reset()
			lineStart = 0
			continue
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if frame.Len() > 0 {
			return fmt.Errorf("%w: provider event is truncated", ErrInvalidOpenAICompletion)
		}
		if !done {
			return fmt.Errorf("%w: provider stream omitted [DONE]", ErrInvalidOpenAICompletion)
		}
		return nil
	}
}

func writeOpenAICompletionStreamFrame(destination io.Writer, frame []byte, publicModel string) (bool, error) {
	payload := completionStreamFrameData(frame)
	if len(payload) == 0 {
		return false, fmt.Errorf("%w: provider event omitted data", ErrInvalidOpenAICompletion)
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		_, err := io.WriteString(destination, "data: [DONE]\n\n")
		return true, err
	}
	rewritten, err := normalizeOpenAICompletionResponse(payload, publicModel, true)
	if err != nil {
		return false, err
	}
	if len(rewritten) > maxOpenAICompletionBytes-len("data: \n\n") {
		return false, fmt.Errorf("%w: provider event exceeds 16 MiB", ErrInvalidOpenAICompletion)
	}
	if _, err = destination.Write(append(append([]byte("data: "), rewritten...), '\n', '\n')); err != nil {
		return false, err
	}
	return false, nil
}

func completionStreamFrameData(frame []byte) []byte {
	var data bytes.Buffer
	for len(frame) > 0 {
		end := bytes.IndexByte(frame, '\n')
		line := frame
		if end >= 0 {
			line, frame = frame[:end], frame[end+1:]
		} else {
			frame = nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
	}
	return data.Bytes()
}
