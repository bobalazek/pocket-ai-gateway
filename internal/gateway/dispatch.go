package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/protocol"
)

var errUpstreamResponseInterrupted = protocol.ErrUpstreamResponseInterrupted

func (handler *Handler) settle(attemptID string, input usage.SettlementInput) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := handler.usage.Settle(ctx, attemptID, input)
		cancel()
		if err == nil {
			return nil
		}
		if permanentSettlementError(err) {
			return err
		}
		last = err
		if attempt < 4 {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
		}
	}
	return last
}

func permanentSettlementError(err error) bool {
	return errors.Is(err, usage.ErrConflict) || errors.Is(err, usage.ErrNotFound)
}

func (handler *Handler) dispatch(response http.ResponseWriter, request *http.Request, target providers.Target, relative string, body []byte, stream bool, dialect, publicModel string, captureStreamTail bool, releaseDispatch func()) (int, []byte, error) {
	released := false
	release := func() {
		if !released {
			released = true
			releaseDispatch()
		}
	}
	defer release()
	endpoint, err := joinURL(target.BaseURL, relative)
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider URL is invalid")
		return 0, nil, err
	}
	upstream, err := http.NewRequestWithContext(request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	requestContentType := "application/json"
	if multipart, ok := multipartRequest(request); ok {
		requestContentType = multipart.contentType
	}
	upstream.Header.Set("Content-Type", requestContentType)
	setProviderCredential(upstream, target.Adapter, target.Preset, target.Credential)
	copyProtocolHeaders(upstream.Header, request.Header, target.Adapter)
	client := safeClient(time.Duration(target.TimeoutMS)*time.Millisecond, target.AllowPrivateNetwork)
	result, err := client.Do(upstream)
	release()
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider request failed")
		return 0, nil, err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(result.Body, (1<<20)+1))
		if readErr != nil || len(raw) > 1<<20 || !writeNativeUpstreamError(response, dialect, result.StatusCode, raw) {
			handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider rejected the request")
		}
		return result.StatusCode, nil, nil
	}
	contentType := result.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if !stream {
		raw, readErr := io.ReadAll(io.LimitReader(result.Body, maxInferenceBody+1))
		if readErr != nil || len(raw) > maxInferenceBody {
			handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider response exceeds 16 MiB")
			return result.StatusCode, nil, errors.New("provider response exceeds 16 MiB")
		}
		if relative == "completions" {
			raw, readErr = protocol.NormalizeOpenAICompletionResponse(raw, publicModel)
			if readErr != nil {
				handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider returned an invalid Completion response")
				return result.StatusCode, nil, readErr
			}
		}
		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(result.StatusCode)
		_, err = response.Write(raw)
		return result.StatusCode, raw, err
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(result.StatusCode)
	flusher, ok := response.(http.Flusher)
	if !ok {
		return result.StatusCode, nil, errors.New("streaming is unsupported by the response writer")
	}
	capture := &limitedCapture{limit: 8 << 20}
	captureWriter := io.Writer(capture)
	var tail *tailCapture
	var window *headTailCapture
	if relative == "audio/transcriptions" {
		tail = &tailCapture{limit: maxInferenceBody}
		captureWriter = tail
	} else if captureStreamTail {
		window = &headTailCapture{head: limitedCapture{limit: 1 << 20}, tail: tailCapture{limit: maxInferenceBody}}
		captureWriter = window
	}
	if relative == "completions" {
		err = protocol.CopyOpenAICompletionStream(io.MultiWriter(flushWriter{writer: response, flusher: flusher}, captureWriter), result.Body, publicModel)
	} else if dialect == "anthropic" {
		err = copyAnthropicStream(io.MultiWriter(flushWriter{writer: response, flusher: flusher}, captureWriter), result.Body, publicModel)
	} else {
		_, err = io.Copy(flushWriter{writer: response, flusher: flusher}, io.TeeReader(result.Body, captureWriter))
	}
	raw := capture.Bytes()
	if tail != nil {
		raw = tail.Bytes()
		if err == nil {
			err = validateAudioTranscriptionStream(contentType, raw)
		}
	} else if window != nil {
		raw = window.Bytes()
	}
	return result.StatusCode, raw, err
}

func (handler *Handler) dispatchTranslated(response http.ResponseWriter, request *http.Request, target providers.Target, relative string, body []byte, dialect, publicModel string, stream bool, releaseDispatch func()) (int, []byte, error) {
	released := false
	release := func() {
		if !released {
			released = true
			releaseDispatch()
		}
	}
	defer release()
	endpoint, err := joinURL(target.BaseURL, relative)
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider URL is invalid")
		return 0, nil, err
	}
	upstream, err := http.NewRequestWithContext(request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	upstream.Header.Set("Content-Type", "application/json")
	setProviderCredential(upstream, target.Adapter, target.Preset, target.Credential)
	if target.Adapter == "anthropic" {
		upstream.Header.Set("anthropic-version", "2023-06-01")
	}
	result, err := safeClient(time.Duration(target.TimeoutMS)*time.Millisecond, target.AllowPrivateNetwork).Do(upstream)
	release()
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider request failed")
		return 0, nil, err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(result.Body, 1<<20))
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider rejected the request")
		return result.StatusCode, nil, nil
	}
	if stream {
		return protocol.TranslateStream(response, result.Body, dialect, target.Adapter, publicModel)
	}
	raw, err := io.ReadAll(io.LimitReader(result.Body, (16<<20)+1))
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider response could not be translated")
		return result.StatusCode, nil, fmt.Errorf("%w: %v", errUpstreamResponseInterrupted, err)
	}
	if len(raw) > 16<<20 {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider response could not be translated")
		return result.StatusCode, nil, errors.New("translated response exceeds 16 MiB")
	}
	translated, err := protocol.TranslateResponse(dialect, target.Adapter, publicModel, raw)
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadGateway, "translation_error", "Provider response could not be translated")
		return result.StatusCode, nil, err
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, err = response.Write(translated)
	return http.StatusOK, raw, err
}

func joinURL(base, relative string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(relative)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(reference.Path, "/")
	parsed.RawQuery = reference.RawQuery
	return parsed.String(), nil
}
func setProviderCredential(request *http.Request, adapter, preset, credential string) {
	if credential == "" {
		return
	}
	if preset == "azure-openai" {
		request.Header.Set("api-key", credential)
		return
	}
	switch adapter {
	case "anthropic":
		request.Header.Set("x-api-key", credential)
	case "gemini":
		request.Header.Set("x-goog-api-key", credential)
	default:
		request.Header.Set("Authorization", "Bearer "+credential)
	}
}
func copyProtocolHeaders(destination, source http.Header, adapter string) {
	if adapter == "anthropic" {
		for _, name := range []string{"anthropic-version", "anthropic-beta"} {
			if value := source.Get(name); value != "" {
				destination.Set(name, value)
			}
		}
	}
}
func safeClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: min(timeout, 10*time.Second)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if allowPrivate || ip.IsGlobalUnicast() && !ip.IsPrivate() {
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
		}
		return nil, errors.New("provider destination is not allowed")
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects are disabled") }}
}

type limitedCapture struct {
	bytes.Buffer
	limit    int64
	overflow bool
}

type tailCapture struct {
	data        []byte
	limit, size int
	next        int
}

type headTailCapture struct {
	head  limitedCapture
	tail  tailCapture
	total int64
}

func (capture *headTailCapture) Write(value []byte) (int, error) {
	capture.total += int64(len(value))
	_, _ = capture.head.Write(value)
	return capture.tail.Write(value)
}

func (capture *headTailCapture) Bytes() []byte {
	tail := capture.tail.Bytes()
	if capture.total <= int64(capture.tail.limit) {
		return tail
	}
	head := completeSSEPrefix(capture.head.Bytes())
	tail = completeSSETail(tail)
	result := make([]byte, 0, len(head)+len(tail))
	result = append(result, head...)
	return append(result, tail...)
}

func completeSSEPrefix(value []byte) []byte {
	lf, crlf := bytes.LastIndex(value, []byte("\n\n")), bytes.LastIndex(value, []byte("\r\n\r\n"))
	if crlf > lf {
		return value[:crlf+4]
	}
	if lf >= 0 {
		return value[:lf+2]
	}
	return nil
}

func completeSSETail(value []byte) []byte {
	lf, crlf := bytes.Index(value, []byte("\n\n")), bytes.Index(value, []byte("\r\n\r\n"))
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return value[crlf+4:]
	}
	if lf >= 0 {
		return value[lf+2:]
	}
	return value
}

func (capture *tailCapture) Write(value []byte) (int, error) {
	count := len(value)
	if capture.limit <= 0 {
		return count, nil
	}
	if count >= capture.limit {
		capture.data = append(capture.data[:0], value[count-capture.limit:]...)
		capture.size, capture.next = capture.limit, 0
		return count, nil
	}
	if capture.size < capture.limit {
		growth := min(len(value), capture.limit-capture.size)
		capture.data = append(capture.data, value[:growth]...)
		capture.size += growth
		capture.next = capture.size % capture.limit
		value = value[growth:]
	}
	for len(value) > 0 {
		written := copy(capture.data[capture.next:], value)
		capture.next = (capture.next + written) % capture.limit
		capture.size = min(capture.limit, capture.size+written)
		value = value[written:]
	}
	return count, nil
}

func (capture *tailCapture) Bytes() []byte {
	if capture.size < capture.limit {
		return append([]byte(nil), capture.data[:capture.size]...)
	}
	value := make([]byte, capture.limit)
	copy(value, capture.data[capture.next:])
	copy(value[capture.limit-capture.next:], capture.data[:capture.next])
	return value
}

func (capture *limitedCapture) Write(value []byte) (int, error) {
	original := len(value)
	remaining := capture.limit - int64(capture.Len())
	if remaining <= 0 {
		capture.overflow = true
		return original, nil
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		capture.overflow = true
	}
	_, _ = capture.Buffer.Write(value)
	return original, nil
}
