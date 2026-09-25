package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const pocketAIRequestIDHeader = "X-Pocket-AI-Request-ID"

type attemptWriter struct {
	destination          http.ResponseWriter
	header               http.Header
	status               int
	body                 bytes.Buffer
	streaming, committed bool
	firstWrite           time.Time
	bufferLimit          int64
}

func newAttemptWriter(destination http.ResponseWriter, streaming bool, bufferLimit int64) *attemptWriter {
	return &attemptWriter{destination: destination, header: make(http.Header), streaming: streaming, bufferLimit: bufferLimit}
}
func (writer *attemptWriter) Header() http.Header { return writer.header }
func (writer *attemptWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
}
func (writer *attemptWriter) Write(value []byte) (int, error) {
	if writer.firstWrite.IsZero() {
		writer.firstWrite = time.Now()
	}
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.committed {
		return writer.destination.Write(value)
	}
	if writer.bufferLimit > 0 && int64(writer.body.Len()+len(value)) > writer.bufferLimit {
		return 0, errors.New("buffered response exceeds limit")
	}
	return writer.body.Write(value)
}
func (writer *attemptWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.streaming && writer.status >= 200 && writer.status < 300 {
		writer.commitHeader()
		if flusher, ok := writer.destination.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}
func (writer *attemptWriter) Committed() bool { return writer.committed }
func (writer *attemptWriter) FirstByte(started time.Time) time.Duration {
	if writer.firstWrite.IsZero() {
		return 0
	}
	return writer.firstWrite.Sub(started)
}

// unixMilliOrNil records an optional event time, such as the first response byte.
func unixMilliOrNil(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	milliseconds := value.UnixMilli()
	return &milliseconds
}

func (writer *attemptWriter) commitHeader() {
	if writer.committed {
		return
	}
	requestID := writer.destination.Header().Get(pocketAIRequestIDHeader)
	for name, values := range writer.header {
		writer.destination.Header()[name] = append([]string(nil), values...)
	}
	if requestID != "" {
		writer.destination.Header().Set(pocketAIRequestIDHeader, requestID)
	}
	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	writer.destination.WriteHeader(status)
	writer.committed = true
	if writer.body.Len() > 0 {
		_, _ = writer.destination.Write(writer.body.Bytes())
		writer.body.Reset()
	}
}
func (writer *attemptWriter) Commit() { writer.commitHeader() }

func (writer *attemptWriter) Reset() {
	writer.header = make(http.Header)
	writer.status = 0
	writer.body.Reset()
}

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (writer flushWriter) Write(value []byte) (int, error) {
	count, err := writer.writer.Write(value)
	writer.flusher.Flush()
	return count, err
}

// requestStreamUsage asks an OpenAI Chat or Completions stream to report usage so the attempt can be
// accounted. It returns true when the client did not ask for usage, so the usage-only chunk must be hidden.
func requestStreamUsage(envelope map[string]json.RawMessage, applies bool) bool {
	if !applies {
		return false
	}
	var options map[string]json.RawMessage
	_ = json.Unmarshal(envelope["stream_options"], &options)
	var include bool
	if json.Unmarshal(options["include_usage"], &include) == nil && include {
		return false
	}
	if options == nil {
		options = make(map[string]json.RawMessage)
	}
	options["include_usage"] = json.RawMessage("true")
	envelope["stream_options"], _ = json.Marshal(options)
	return true
}

// usageChunkFilter removes the usage-only OpenAI stream chunk that the gateway requested for accounting.
type usageChunkFilter struct {
	http.ResponseWriter
	pending []byte
}

func (filter *usageChunkFilter) Write(value []byte) (int, error) {
	if len(filter.pending)+len(value) > maxInferenceBody {
		return 0, errors.New("provider stream event exceeds 16 MiB")
	}
	filter.pending = append(filter.pending, value...)
	for {
		end := sseFrameEnd(filter.pending)
		if end < 0 {
			return len(value), nil
		}
		if !openAIUsageOnlyChunk(filter.pending[:end]) {
			if _, err := filter.ResponseWriter.Write(filter.pending[:end]); err != nil {
				return 0, err
			}
		}
		filter.pending = append(filter.pending[:0], filter.pending[end:]...)
	}
}

func (filter *usageChunkFilter) Flush() {
	if flusher, ok := filter.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Finish writes any trailing bytes that did not end with a blank line, such as a JSON error body.
func (filter *usageChunkFilter) Finish() error {
	if len(filter.pending) == 0 {
		return nil
	}
	_, err := filter.ResponseWriter.Write(filter.pending)
	filter.pending = nil
	return err
}

func sseFrameEnd(buffer []byte) int {
	lf, crlf := bytes.Index(buffer, []byte("\n\n")), bytes.Index(buffer, []byte("\r\n\r\n"))
	switch {
	case lf >= 0 && (crlf < 0 || lf < crlf):
		return lf + 2
	case crlf >= 0:
		return crlf + 4
	}
	return -1
}

func openAIUsageOnlyChunk(frame []byte) bool {
	var chunk struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	payload := conversationStreamFrameData(frame)
	return json.Unmarshal(payload, &chunk) == nil && chunk.Choices != nil && len(chunk.Choices) == 0 && len(chunk.Usage) > 0 && string(chunk.Usage) != "null"
}

// chatStreamObserver watches a native Chat Completions stream for a provider error chunk and for the
// terminal [DONE] marker, so an interrupted or failed stream is not accounted as a success.
type chatStreamObserver struct {
	line          []byte
	done, errored bool
}

func (observer *chatStreamObserver) Write(value []byte) (int, error) {
	for _, character := range value {
		if character != '\n' {
			if len(observer.line) >= maxInferenceBody {
				return 0, errors.New("provider stream event exceeds 16 MiB")
			}
			observer.line = append(observer.line, character)
			continue
		}
		observer.inspect(bytes.TrimSpace(observer.line))
		observer.line = observer.line[:0]
	}
	return len(value), nil
}

func (observer *chatStreamObserver) inspect(line []byte) {
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	payload = bytes.TrimSpace(payload)
	if bytes.Equal(payload, []byte("[DONE]")) {
		observer.done = true
		return
	}
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return
	}
	var chunk struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &chunk) == nil && len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		observer.errored = true
	}
}

func (observer *chatStreamObserver) result() error {
	observer.inspect(bytes.TrimSpace(observer.line))
	switch {
	case observer.errored:
		return fmt.Errorf("%w: provider stream reported an error", errUpstreamResponseInterrupted)
	case !observer.done:
		return fmt.Errorf("%w: provider stream ended before [DONE]", errUpstreamResponseInterrupted)
	}
	return nil
}
