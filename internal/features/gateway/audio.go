package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxAudioFile       = 25_000_000
	maxAudioUploadBody = 26 << 20
)

type nativeMultipartRequest struct {
	body        []byte
	contentType string
}

type nativeMultipartRequestKey struct{}

func (handler *Handler) audioTranscription(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "openai")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxAudioUploadBody))
	if err != nil {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 26 MiB")
		return
	}
	model, stream, err := validateAudioMultipart(body, request.Header.Get("Content-Type"))
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	envelope, _ := json.Marshal(map[string]any{"model": model, "stream": stream})
	request = request.WithContext(context.WithValue(request.Context(), nativeMultipartRequestKey{}, nativeMultipartRequest{body: body, contentType: request.Header.Get("Content-Type")}))
	handler.forwardAuthorized(response, request, "openai", "audio:transcribe", "audio/transcriptions", model, nil, principal, envelope)
}

func validateAudioMultipart(body []byte, contentType string) (string, bool, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return "", false, errors.New("content type must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
	model, stream, files, models, streams, parts := "", false, 0, 0, 0, 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", false, errors.New("multipart request is invalid")
		}
		parts++
		if parts > 1000 {
			return "", false, errors.New("multipart request has too many fields")
		}
		name := part.FormName()
		if name != "file" && part.FileName() != "" {
			return "", false, errors.New("only the file field may contain an upload")
		}
		if name == "file" {
			files++
			filename := part.FileName()
			count, copyErr := io.Copy(io.Discard, io.LimitReader(part, maxAudioFile+1))
			if copyErr != nil || count == 0 || count > maxAudioFile || filename == "" || !supportedAudioUpload(filename, part.Header.Get("Content-Type")) {
				return "", false, errors.New("file must be a non-empty supported audio upload")
			}
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, (1<<20)+1))
		if readErr != nil || len(value) > 1<<20 {
			return "", false, errors.New("multipart field exceeds 1 MiB")
		}
		switch name {
		case "model":
			models++
			if models > 1 || part.FileName() != "" {
				return "", false, errors.New("model must be provided once as text")
			}
			model = strings.TrimSpace(string(value))
		case "stream":
			streams++
			if streams > 1 || part.FileName() != "" {
				return "", false, errors.New("stream must be provided at most once as text")
			}
			text := strings.TrimSpace(string(value))
			if text == "null" {
				continue
			}
			parsed, parseErr := strconv.ParseBool(text)
			if parseErr != nil {
				return "", false, errors.New("stream must be a boolean")
			}
			stream = parsed
		}
	}
	if model == "" {
		return "", false, errors.New("model is required")
	}
	if files != 1 {
		return "", false, errors.New("exactly one audio file is required")
	}
	return model, stream, nil
}

func supportedAudioUpload(filename, contentType string) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	if map[string]bool{".flac": true, ".mp3": true, ".mp4": true, ".mpeg": true, ".mpga": true, ".m4a": true, ".ogg": true, ".wav": true, ".webm": true}[extension] {
		return true
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return map[string]bool{"audio/flac": true, "audio/mpeg": true, "audio/mp4": true, "audio/ogg": true, "audio/wav": true, "audio/x-wav": true, "audio/webm": true}[mediaType]
}

func rewriteMultipartModel(value nativeMultipartRequest, model string) ([]byte, error) {
	_, parameters, err := mime.ParseMediaType(value.contentType)
	if err != nil {
		return nil, err
	}
	reader := multipart.NewReader(bytes.NewReader(value.body), parameters["boundary"])
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	if err := writer.SetBoundary(parameters["boundary"]); err != nil {
		return nil, err
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		destination, err := writer.CreatePart(part.Header)
		if err != nil {
			return nil, err
		}
		if part.FormName() == "model" {
			_, err = io.WriteString(destination, model)
		} else {
			_, err = io.Copy(destination, part)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func multipartRequest(request *http.Request) (nativeMultipartRequest, bool) {
	value, ok := request.Context().Value(nativeMultipartRequestKey{}).(nativeMultipartRequest)
	return value, ok
}

func validateAudioTranscriptionStream(contentType string, raw []byte) error {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "text/event-stream" {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("event: error")) || bytes.Contains(raw, []byte("\nevent: error")) {
		return errUpstreamResponseInterrupted
	}
	completed := false
	for _, object := range responseObjects(raw) {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(object, &event) == nil {
			if event.Type == "error" {
				return errUpstreamResponseInterrupted
			}
			completed = completed || event.Type == "transcript.text.done"
		}
	}
	if completed {
		return nil
	}
	return errUpstreamResponseInterrupted
}
