package gateway

import (
	"bytes"
	"errors"
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
