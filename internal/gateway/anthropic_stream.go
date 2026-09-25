package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var errAnthropicStreamInvalid = errors.New("invalid Anthropic stream")

func copyAnthropicStream(destination io.Writer, source io.Reader, publicModel string) error {
	reader := bufio.NewReader(source)
	var frame bytes.Buffer
	lineStart := 0
	stopped := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maxInferenceBody-frame.Len() {
			return fmt.Errorf("%w: provider event exceeds 16 MiB", errAnthropicStreamInvalid)
		}
		frame.Write(fragment)
		if err == nil {
			line := frame.Bytes()[lineStart:]
			lineStart = frame.Len()
			if !bytes.Equal(line, []byte("\n")) && !bytes.Equal(line, []byte("\r\n")) {
				continue
			}
			event, err := writeAnthropicStreamFrame(destination, frame.Bytes(), publicModel)
			if err != nil {
				return err
			}
			// A provider error or a stream without message_stop is not a complete, fully-billed Message.
			if event == "error" {
				return fmt.Errorf("%w: provider stream reported an error", errUpstreamResponseInterrupted)
			}
			stopped = stopped || event == "message_stop"
			frame.Reset()
			lineStart = 0
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		if frame.Len() > 0 {
			return fmt.Errorf("%w: provider event is truncated", errAnthropicStreamInvalid)
		}
		if !stopped {
			return fmt.Errorf("%w: provider stream ended before message_stop", errUpstreamResponseInterrupted)
		}
		return nil
	}
}

// writeAnthropicStreamFrame forwards one SSE frame and returns its event type.
func writeAnthropicStreamFrame(destination io.Writer, frame []byte, publicModel string) (string, error) {
	payload := conversationStreamFrameData(frame)
	if len(payload) == 0 {
		_, err := destination.Write(frame)
		return anthropicStreamEvent(frame), err
	}
	var value map[string]json.RawMessage
	decodeErr := json.Unmarshal(payload, &value)
	var eventType string
	if decodeErr == nil {
		_ = json.Unmarshal(value["type"], &eventType)
	}
	if eventType == "" {
		eventType = anthropicStreamEvent(frame)
	}
	if eventType != "message_start" && anthropicStreamEvent(frame) != "message_start" {
		_, err := destination.Write(frame)
		return eventType, err
	}
	if decodeErr != nil {
		return "", fmt.Errorf("%w: message_start contains invalid JSON", errAnthropicStreamInvalid)
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(value["message"], &message) != nil || message == nil {
		return "", fmt.Errorf("%w: message_start omitted message", errAnthropicStreamInvalid)
	}
	message["model"], _ = json.Marshal(publicModel)
	value["message"], _ = json.Marshal(message)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var rewritten bytes.Buffer
	walkConversationStreamLines(bytes.TrimRight(frame, "\r\n"), func(line []byte) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte("data:")) {
			rewritten.Write(line)
			rewritten.WriteByte('\n')
		}
	})
	rewritten.WriteString("data: ")
	rewritten.Write(encoded)
	rewritten.WriteString("\n\n")
	_, err = destination.Write(rewritten.Bytes())
	return "message_start", err
}

func anthropicStreamEvent(frame []byte) string {
	var event string
	walkConversationStreamLines(frame, func(line []byte) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("event:")) {
			event = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:"))))
		}
	})
	return event
}
