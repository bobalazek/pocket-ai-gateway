package protocol

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxOpenAIImageStreamEventBytes = 16 << 20

var ErrInvalidOpenAIImageStream = errors.New("invalid OpenAI image stream")

// CopyOpenAIImageGenerationStream validates and forwards complete image-generation
// SSE events. It returns only the terminal usage document so callers do not need
// to retain base64 image data for accounting.
func CopyOpenAIImageGenerationStream(destination io.Writer, source io.Reader, partialImages int) ([]byte, error) {
	if partialImages < 0 || partialImages > 3 {
		return nil, fmt.Errorf("%w: partial image count must be between 0 and 3", ErrInvalidOpenAIImageStream)
	}
	reader := bufio.NewReader(source)
	var frame bytes.Buffer
	lineStart := 0
	completed := false
	seenPartials := [3]bool{}
	var usage []byte
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > maxOpenAIImageStreamEventBytes-frame.Len() {
			return nil, fmt.Errorf("%w: provider event exceeds 16 MiB", ErrInvalidOpenAIImageStream)
		}
		frame.Write(fragment)
		if readErr == nil {
			line := frame.Bytes()[lineStart:]
			lineStart = frame.Len()
			if !bytes.Equal(line, []byte("\n")) && !bytes.Equal(line, []byte("\r\n")) {
				continue
			}
			if completed {
				return nil, fmt.Errorf("%w: provider sent an event after completion", ErrInvalidOpenAIImageStream)
			}
			terminalUsage, partialIndex, terminal, err := validateOpenAIImageStreamFrame(frame.Bytes(), partialImages)
			if err != nil {
				return nil, err
			}
			if partialIndex >= 0 {
				if seenPartials[partialIndex] {
					return nil, fmt.Errorf("%w: provider repeated partial_image_index", ErrInvalidOpenAIImageStream)
				}
				seenPartials[partialIndex] = true
			}
			if _, err = destination.Write(frame.Bytes()); err != nil {
				return nil, err
			}
			if terminal {
				usage, completed = terminalUsage, true
			}
			frame.Reset()
			lineStart = 0
			continue
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if frame.Len() > 0 {
			return nil, fmt.Errorf("%w: provider event is truncated", ErrInvalidOpenAIImageStream)
		}
		if !completed {
			return nil, fmt.Errorf("%w: provider stream omitted image_generation.completed", ErrInvalidOpenAIImageStream)
		}
		return usage, nil
	}
}

func validateOpenAIImageStreamFrame(frame []byte, partialImages int) ([]byte, int64, bool, error) {
	event, payload, err := openAIImageStreamFrame(frame)
	if err != nil {
		return nil, -1, false, err
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(payload, &value) != nil || value == nil {
		return nil, -1, false, fmt.Errorf("%w: provider event data must be a JSON object", ErrInvalidOpenAIImageStream)
	}
	eventType, ok := imageStreamString(value["type"])
	if !ok || eventType != event {
		return nil, -1, false, fmt.Errorf("%w: provider event name and type do not match", ErrInvalidOpenAIImageStream)
	}
	if event != "image_generation.partial_image" && event != "image_generation.completed" {
		return nil, -1, false, fmt.Errorf("%w: unsupported provider event %q", ErrInvalidOpenAIImageStream, event)
	}
	if err := validateOpenAIImageFields(value); err != nil {
		return nil, -1, false, err
	}
	if event == "image_generation.partial_image" {
		index, ok := imageStreamInteger(value["partial_image_index"])
		if !ok || index < 0 || index >= int64(partialImages) {
			return nil, -1, false, fmt.Errorf("%w: partial_image_index exceeds the requested partial image count", ErrInvalidOpenAIImageStream)
		}
		return nil, index, false, nil
	}
	usage := value["usage"]
	if err := validateOpenAIImageUsage(usage); err != nil {
		return nil, -1, false, err
	}
	metadata := make([]byte, 0, len(usage)+11)
	metadata = append(metadata, `{"usage":`...)
	metadata = append(metadata, usage...)
	metadata = append(metadata, '}')
	return metadata, -1, true, nil
}

func openAIImageStreamFrame(frame []byte) (string, []byte, error) {
	var event string
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
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			if event != "" {
				return "", nil, fmt.Errorf("%w: provider event declared its name more than once", ErrInvalidOpenAIImageStream)
			}
			event = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:"))))
		case bytes.HasPrefix(line, []byte("data:")):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	if event == "" {
		return "", nil, fmt.Errorf("%w: provider event omitted its name", ErrInvalidOpenAIImageStream)
	}
	payload := data.Bytes()
	if len(payload) == 0 {
		return "", nil, fmt.Errorf("%w: provider event omitted data", ErrInvalidOpenAIImageStream)
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		return "", nil, fmt.Errorf("%w: image streams must end with image_generation.completed", ErrInvalidOpenAIImageStream)
	}
	return event, payload, nil
}

func validateOpenAIImageFields(value map[string]json.RawMessage) error {
	image, ok := imageStreamString(value["b64_json"])
	if !ok || image == "" {
		return fmt.Errorf("%w: provider event has invalid b64_json", ErrInvalidOpenAIImageStream)
	}
	if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(image))); err != nil {
		return fmt.Errorf("%w: provider event has invalid b64_json", ErrInvalidOpenAIImageStream)
	}
	if created, ok := imageStreamInteger(value["created_at"]); !ok || created < 0 {
		return fmt.Errorf("%w: provider event has invalid created_at", ErrInvalidOpenAIImageStream)
	}
	for name, allowed := range map[string]map[string]bool{
		"background":    {"transparent": true, "opaque": true, "auto": true},
		"output_format": {"png": true, "webp": true, "jpeg": true},
		"quality":       {"low": true, "medium": true, "high": true, "xhigh": true, "max": true, "auto": true},
	} {
		text, ok := imageStreamString(value[name])
		if !ok || !allowed[text] {
			return fmt.Errorf("%w: provider event has invalid %s", ErrInvalidOpenAIImageStream, name)
		}
	}
	if size, ok := imageStreamString(value["size"]); !ok || size == "" {
		return fmt.Errorf("%w: provider event has invalid size", ErrInvalidOpenAIImageStream)
	}
	return nil
}

func validateOpenAIImageUsage(raw json.RawMessage) error {
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil || usage == nil {
		return fmt.Errorf("%w: completed event has invalid usage", ErrInvalidOpenAIImageStream)
	}
	values := map[string]int64{}
	for _, name := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		value, ok := imageStreamInteger(usage[name])
		if !ok || value < 0 {
			return fmt.Errorf("%w: completed event has invalid usage.%s", ErrInvalidOpenAIImageStream, name)
		}
		values[name] = value
	}
	var details map[string]json.RawMessage
	if json.Unmarshal(usage["input_tokens_details"], &details) != nil || details == nil {
		return fmt.Errorf("%w: completed event has invalid usage.input_tokens_details", ErrInvalidOpenAIImageStream)
	}
	detailValues := map[string]int64{}
	for _, name := range []string{"image_tokens", "text_tokens"} {
		value, ok := imageStreamInteger(details[name])
		if !ok || value < 0 {
			return fmt.Errorf("%w: completed event has invalid usage.input_tokens_details.%s", ErrInvalidOpenAIImageStream, name)
		}
		detailValues[name] = value
	}
	if values["input_tokens"] > 9_007_199_254_740_991-values["output_tokens"] || values["total_tokens"] != values["input_tokens"]+values["output_tokens"] {
		return fmt.Errorf("%w: completed event has inconsistent total token usage", ErrInvalidOpenAIImageStream)
	}
	if detailValues["image_tokens"] > 9_007_199_254_740_991-detailValues["text_tokens"] || values["input_tokens"] != detailValues["image_tokens"]+detailValues["text_tokens"] {
		return fmt.Errorf("%w: completed event has inconsistent input token usage", ErrInvalidOpenAIImageStream)
	}
	return nil
}

func imageStreamInteger(raw json.RawMessage) (int64, bool) {
	var value *int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return 0, false
	}
	return *value, true
}

func imageStreamString(raw json.RawMessage) (string, bool) {
	var value *string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return "", false
	}
	return *value, true
}
