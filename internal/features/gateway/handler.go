package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const maxInferenceBody = 16 << 20

type Handler struct {
	keys      *keys.Service
	providers *providers.Service
	usage     *usage.Service
}

func New(keyService *keys.Service, providerService *providers.Service, usageService *usage.Service) *Handler {
	return &Handler{keys: keyService, providers: providerService, usage: usageService}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/openai/v1/models", handler.openAIModels)
	mux.HandleFunc("GET /api/openai/v1/models/{model}", handler.openAIModel)
	mux.HandleFunc("POST /api/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "chat:generate", "chat/completions")
	})
	mux.HandleFunc("POST /api/openai/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "responses", "responses:generate", "responses")
	})
	mux.HandleFunc("POST /api/openai/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "embeddings:generate", "embeddings")
	})
	mux.HandleFunc("GET /api/anthropic/v1/models", handler.anthropicModels)
	mux.HandleFunc("GET /api/anthropic/v1/models/{model}", handler.anthropicModel)
	mux.HandleFunc("POST /api/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "chat:generate", "messages")
	})
	mux.HandleFunc("POST /api/anthropic/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "tokens:count", "messages/count_tokens")
	})
	mux.HandleFunc("GET /api/gemini/v1beta/models", handler.geminiModels)
	mux.HandleFunc("GET /api/gemini/v1beta/models/{model}", handler.geminiModel)
	mux.HandleFunc("POST /api/gemini/v1beta/models/{action...}", handler.geminiAction)
}

func (handler *Handler) forward(response http.ResponseWriter, request *http.Request, dialect, scope, upstreamPath string) {
	clientOperation := upstreamPath
	principal, ok := handler.authenticate(response, request, dialect)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, dialect, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	originalBodyBytes := int64(len(body))
	requestToolCount := countRequestTools(dialect, body)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var publicID string
	if raw := envelope["model"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &publicID)
	}
	if publicID == "" {
		handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	target, err := handler.providers.Target(request.Context(), publicID)
	if err != nil {
		handler.writeError(response, dialect, http.StatusNotFound, "model_not_found", "Model is unavailable")
		return
	}
	native := nativeAdapter(dialect, target.Adapter) || dialect == "responses" && (target.Adapter == "openai" || target.Adapter == "openai_compatible")
	if !hasCapability(target.Capabilities, scope) || (!native && scope != "chat:generate" && scope != "responses:generate") {
		handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_operation", "Model does not support this operation")
		return
	}
	if !principal.Allows(scope, publicID, target.TargetConnectionID) {
		handler.writeError(response, dialect, http.StatusNotFound, "model_not_found", "Model is unavailable to this key")
		return
	}
	if dialect == "responses" {
		if err := validateStatelessResponses(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", err.Error())
			return
		}
	}
	stream := false
	_ = json.Unmarshal(envelope["stream"], &stream)
	if dialect == "anthropic" && stream && !native {
		handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", "Anthropic streaming requires an Anthropic-compatible target so input usage is known before the first event")
		return
	}
	inputEstimate := originalBodyBytes
	outputEstimate := maximumOutput(envelope)
	if !native && target.Adapter == "anthropic" && outputEstimate == 0 {
		outputEstimate = 4096
	}
	batchItems := int64(0)
	if upstreamPath == "embeddings" {
		batchItems = jsonCardinality(envelope["input"])
	}
	if native {
		encodedModel, _ := json.Marshal(target.UpstreamID)
		envelope["model"] = encodedModel
		body, err = json.Marshal(envelope)
	} else {
		upstreamPath, body, err = translateRequest(dialect, target.Adapter, body, target.UpstreamID)
	}
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	generation := scope == "chat:generate" || scope == "responses:generate"
	admission, err := handler.usage.Admit(request.Context(), usage.AdmissionInput{KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: clientOperation, TargetOperation: upstreamPath, Scope: scope, Dialect: dialect, TargetDialect: target.Adapter, TranslationApplied: !native, RequestToolCount: requestToolCount, BodyBytes: originalBodyBytes, BatchItems: batchItems, EstimatedInputTokens: inputEstimate, EstimatedOutputTokens: outputEstimate, EnforceOutputBound: generation, OutputBounded: !generation || outputEstimate > 0})
	if err != nil {
		handler.writeAdmissionError(response, dialect, err)
		return
	}
	releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
	if !current {
		_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
		handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Provider configuration changed before dispatch")
		return
	}
	if err := handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
		releaseDispatch()
		_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
		handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Request could not be dispatched")
		return
	}
	var result int
	var raw []byte
	var copyErr error
	if native {
		result, raw, copyErr = handler.dispatch(response, request, target, upstreamPath, body, stream, dialect, releaseDispatch)
	} else {
		result, raw, copyErr = handler.dispatchTranslated(response, request, target, upstreamPath, body, dialect, publicID, stream, releaseDispatch)
	}
	state, status := "succeeded", "provider_reported"
	final := true
	accountDialect := dialect
	if !native {
		accountDialect = target.Adapter
		if accountDialect == "openai_compatible" {
			accountDialect = "openai"
		}
	}
	inputTokens, outputTokens, cost := parseUsage(accountDialect, raw)
	toolCalls, toolStatus := parseToolMetadata(accountDialect, raw)
	if inputTokens == nil || outputTokens == nil {
		status, cost = "unknown", nil
	}
	if scope == "tokens:count" {
		if inputTokens != nil {
			zero := int64(0)
			cost = &zero
		}
	}
	if copyErr != nil || result == 0 || result >= 400 {
		state, status = "failed", "unknown"
		inputTokens, outputTokens, cost = nil, nil, nil
		if result >= 400 && result < 500 && copyErr == nil {
			zero := int64(0)
			status, inputTokens, outputTokens, cost = "estimated", &zero, &zero, &zero
		}
	}
	if copyErr != nil && toolCalls > 0 {
		toolStatus = "incomplete"
	}
	if errors.Is(request.Context().Err(), context.Canceled) {
		state = "interrupted_unknown"
		status = "unknown"
	}
	handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: inputTokens, OutputTokens: outputTokens, CostNanos: cost, ResponseToolCallCount: toolCalls, ToolCallStatus: toolStatus, FinalRequest: final})
}

func validateStatelessResponses(envelope map[string]json.RawMessage) error {
	var stored bool
	if raw, exists := envelope["store"]; !exists || json.Unmarshal(raw, &stored) != nil || stored {
		return errors.New("store:false is required")
	}
	for _, field := range []string{"background", "conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("false")) && !bytes.Equal(raw, []byte(`""`)) {
			return errors.New(field + " is not supported by stateless Responses")
		}
	}
	if raw := envelope["tools"]; len(raw) > 0 {
		var tools []map[string]any
		if json.Unmarshal(raw, &tools) != nil {
			return errors.New("tools must be an array")
		}
		for _, tool := range tools {
			if kind, _ := tool["type"].(string); kind != "function" {
				return errors.New("only function tools are supported by stateless Responses")
			}
		}
	}
	return nil
}

func (handler *Handler) geminiAction(response http.ResponseWriter, request *http.Request) {
	action := request.PathValue("action")
	colon := strings.LastIndex(action, ":")
	if colon < 1 {
		handler.writeError(response, "gemini", http.StatusNotFound, "NOT_FOUND", "Resource not found")
		return
	}
	publicID, name := strings.TrimPrefix(action[:colon], "models/"), action[colon+1:]
	scope := map[string]string{"generateContent": "chat:generate", "streamGenerateContent": "chat:generate", "countTokens": "tokens:count", "embedContent": "embeddings:generate", "batchEmbedContents": "embeddings:generate"}[name]
	if scope == "" {
		handler.writeError(response, "gemini", http.StatusNotFound, "NOT_FOUND", "Operation is unavailable")
		return
	}
	principal, ok := handler.authenticate(response, request, "gemini")
	if !ok {
		return
	}
	target, err := handler.providers.Target(request.Context(), publicID)
	native := err == nil && nativeAdapter("gemini", target.Adapter)
	if err != nil || !hasCapability(target.Capabilities, scope) || (!native && scope != "chat:generate") || !principal.Allows(scope, publicID, target.TargetConnectionID) {
		handler.writeError(response, "gemini", http.StatusNotFound, "NOT_FOUND", "Model is unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "gemini", http.StatusRequestEntityTooLarge, "RESOURCE_EXHAUSTED", "Request body exceeds 16 MiB")
		return
	}
	var object map[string]any
	if json.Unmarshal(body, &object) != nil {
		handler.writeError(response, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Request body must be a JSON object")
		return
	}
	requestToolCount := countRequestTools("gemini", body)
	stream := name == "streamGenerateContent"
	relative, upstreamBody := "", body
	if native {
		relative = "models/" + url.PathEscape(target.UpstreamID) + ":" + name
		if stream {
			relative += "?alt=sse"
		}
	} else {
		translationBody := body
		if stream {
			object["stream"] = true
			translationBody, err = json.Marshal(object)
		}
		if err == nil {
			relative, upstreamBody, err = translateRequest("gemini", target.Adapter, translationBody, target.UpstreamID)
		}
		if err != nil {
			handler.writeError(response, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
	}
	batchItems := int64(0)
	if name == "batchEmbedContents" {
		if values, ok := object["requests"].([]any); ok {
			batchItems = int64(len(values))
		}
	}
	outputEstimate := geminiMaximumOutput(object)
	admission, err := handler.usage.Admit(request.Context(), usage.AdmissionInput{KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: name, TargetOperation: relative, Scope: scope, Dialect: "gemini", TargetDialect: target.Adapter, TranslationApplied: !native, RequestToolCount: requestToolCount, BodyBytes: int64(len(body)), BatchItems: batchItems, EstimatedInputTokens: int64(len(body)), EstimatedOutputTokens: outputEstimate, EnforceOutputBound: scope == "chat:generate", OutputBounded: scope != "chat:generate" || outputEstimate > 0})
	if err != nil {
		handler.writeAdmissionError(response, "gemini", err)
		return
	}
	releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
	if !current {
		_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
		handler.writeError(response, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Provider configuration changed before dispatch")
		return
	}
	if err := handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
		releaseDispatch()
		_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
		handler.writeError(response, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Request could not be dispatched")
		return
	}
	var result int
	var raw []byte
	var copyErr error
	if native {
		result, raw, copyErr = handler.dispatch(response, request, target, relative, upstreamBody, stream, "gemini", releaseDispatch)
	} else {
		result, raw, copyErr = handler.dispatchTranslated(response, request, target, relative, upstreamBody, "gemini", publicID, stream, releaseDispatch)
	}
	state, status := "succeeded", "provider_reported"
	accountDialect := "gemini"
	if !native {
		accountDialect = target.Adapter
		if accountDialect == "openai_compatible" {
			accountDialect = "openai"
		}
	}
	in, out, cost := parseUsage(accountDialect, raw)
	toolCalls, toolStatus := parseToolMetadata(accountDialect, raw)
	if in == nil || out == nil {
		status, cost = "unknown", nil
	}
	if scope == "tokens:count" {
		if in != nil {
			zero := int64(0)
			cost = &zero
		}
	}
	if copyErr != nil || result == 0 || result >= 400 {
		state, status = "failed", "unknown"
		in, out, cost = nil, nil, nil
		if result >= 400 && result < 500 && copyErr == nil {
			zero := int64(0)
			status, in, out, cost = "estimated", &zero, &zero, &zero
		}
	}
	if copyErr != nil && toolCalls > 0 {
		toolStatus = "incomplete"
	}
	if errors.Is(request.Context().Err(), context.Canceled) {
		state, status = "interrupted_unknown", "unknown"
	}
	handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: in, OutputTokens: out, CostNanos: cost, ResponseToolCallCount: toolCalls, ToolCallStatus: toolStatus, FinalRequest: true})
}

func (handler *Handler) settle(attemptID string, input usage.SettlementInput) {
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := handler.usage.Settle(ctx, attemptID, input)
		cancel()
		if err == nil || errors.Is(err, usage.ErrConflict) || errors.Is(err, usage.ErrNotFound) {
			return
		}
		if attempt < 4 {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
		}
	}
}

func (handler *Handler) dispatch(response http.ResponseWriter, request *http.Request, target providers.Target, relative string, body []byte, stream bool, dialect string, releaseDispatch func()) (int, []byte, error) {
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
	setProviderCredential(upstream, target.Adapter, target.Credential)
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
		io.Copy(io.Discard, io.LimitReader(result.Body, 1<<20))
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider rejected the request")
		return result.StatusCode, nil, nil
	}
	contentType := result.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(result.StatusCode)
	capture := &limitedCapture{limit: 8 << 20}
	destination := io.Writer(response)
	if stream {
		flusher, ok := response.(http.Flusher)
		if !ok {
			return result.StatusCode, nil, errors.New("streaming is unsupported by the response writer")
		}
		destination = flushWriter{writer: response, flusher: flusher}
	}
	_, err = io.Copy(destination, io.TeeReader(result.Body, capture))
	return result.StatusCode, capture.Bytes(), err
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
	setProviderCredential(upstream, target.Adapter, target.Credential)
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
		return handler.translateStream(response, result.Body, dialect, target.Adapter, publicModel)
	}
	raw, err := io.ReadAll(io.LimitReader(result.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		handler.writeError(response, dialect, http.StatusBadGateway, "upstream_error", "Provider response could not be translated")
		return result.StatusCode, nil, errors.New("translated response exceeds 16 MiB")
	}
	translated, err := translateResponse(dialect, target.Adapter, publicModel, raw)
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

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (writer flushWriter) Write(value []byte) (int, error) {
	count, err := writer.writer.Write(value)
	writer.flusher.Flush()
	return count, err
}

func (handler *Handler) authenticate(response http.ResponseWriter, request *http.Request, dialect string) (keys.Principal, bool) {
	var token string
	switch dialect {
	case "openai", "responses":
		if len(request.Header.Values("Authorization")) != 1 {
			handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "Provide one API key")
			return keys.Principal{}, false
		}
		value := request.Header.Get("Authorization")
		if strings.HasPrefix(value, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
		}
	case "anthropic":
		if request.Header.Get("anthropic-version") != "2023-06-01" {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request_error", "anthropic-version must be 2023-06-01")
			return keys.Principal{}, false
		}
		if len(request.Header.Values("x-api-key")) != 1 {
			handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "Provide one API key")
			return keys.Principal{}, false
		}
		token = strings.TrimSpace(request.Header.Get("x-api-key"))
	case "gemini":
		header, query := strings.TrimSpace(request.Header.Get("x-goog-api-key")), request.URL.Query().Get("key")
		if len(request.Header.Values("x-goog-api-key")) > 1 || header != "" && query != "" {
			handler.writeError(response, dialect, http.StatusBadRequest, "INVALID_ARGUMENT", "Provide one API key")
			return keys.Principal{}, false
		}
		token = header
		if token == "" {
			token = query
		}
		queryValues := request.URL.Query()
		queryValues.Del("key")
		request.URL.RawQuery = queryValues.Encode()
	}
	if token == "" {
		handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "API key is required")
		return keys.Principal{}, false
	}
	principal, err := handler.keys.Authenticate(request.Context(), token)
	if err != nil {
		handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "API key is invalid")
		return keys.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) allowedModels(response http.ResponseWriter, request *http.Request, dialect string) (keys.Principal, []providers.PublicModel, bool) {
	principal, ok := handler.authenticate(response, request, dialect)
	if !ok {
		return keys.Principal{}, nil, false
	}
	items, err := handler.providers.ListPublicModels(request.Context())
	if err != nil {
		handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Models are unavailable")
		return keys.Principal{}, nil, false
	}
	filtered := items[:0]
	for _, item := range items {
		if modelVisibleInDialect(dialect, item) && principal.Allows("models:read", item.ID, item.TargetConnectionID) {
			filtered = append(filtered, item)
		}
	}
	return principal, filtered, true
}

func modelVisibleInDialect(dialect string, model providers.PublicModel) bool {
	if hasCapability(model.Capabilities, "chat:generate") {
		return true
	}
	if dialect == "openai" && nativeAdapter("openai", model.Adapter) {
		return hasCapability(model.Capabilities, "embeddings:generate")
	}
	if dialect == "gemini" && nativeAdapter("gemini", model.Adapter) {
		return hasCapability(model.Capabilities, "embeddings:generate") || hasCapability(model.Capabilities, "tokens:count")
	}
	return false
}
func (handler *Handler) openAIModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "openai")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"id": m.ID, "object": "model", "created": 0, "owned_by": "pocket-ai-gateway"})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}
func (handler *Handler) openAIModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "openai")
	if !ok {
		return
	}
	for _, m := range items {
		if m.ID == r.PathValue("model") {
			writeJSON(w, map[string]any{"id": m.ID, "object": "model", "created": 0, "owned_by": "pocket-ai-gateway"})
			return
		}
	}
	handler.writeError(w, "openai", http.StatusNotFound, "model_not_found", "Model is unavailable")
}
func (handler *Handler) anthropicModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "anthropic")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"id": m.ID, "type": "model", "display_name": m.Label, "created_at": "1970-01-01T00:00:00Z"})
	}
	first, last := "", ""
	if len(items) > 0 {
		first, last = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(w, map[string]any{"data": data, "has_more": false, "first_id": first, "last_id": last})
}
func (handler *Handler) anthropicModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "anthropic")
	if !ok {
		return
	}
	for _, m := range items {
		if m.ID == r.PathValue("model") {
			writeJSON(w, map[string]any{"id": m.ID, "type": "model", "display_name": m.Label, "created_at": "1970-01-01T00:00:00Z"})
			return
		}
	}
	handler.writeError(w, "anthropic", http.StatusNotFound, "not_found_error", "Model is unavailable")
}
func (handler *Handler) geminiModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "gemini")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"name": "models/" + m.ID, "displayName": m.Label, "description": m.Description, "supportedGenerationMethods": geminiMethods(m.Capabilities)})
	}
	writeJSON(w, map[string]any{"models": data})
}
func (handler *Handler) geminiModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "gemini")
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.PathValue("model"), "models/")
	for _, m := range items {
		if m.ID == id {
			writeJSON(w, map[string]any{"name": "models/" + m.ID, "displayName": m.Label, "description": m.Description, "supportedGenerationMethods": geminiMethods(m.Capabilities)})
			return
		}
	}
	handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "Model is unavailable")
}

func (handler *Handler) writeAdmissionError(w http.ResponseWriter, dialect string, err error) {
	var denial *usage.Denial
	if errors.As(err, &denial) {
		if denial.RetryAfterSecond != nil {
			w.Header().Set("Retry-After", strconv.FormatInt(*denial.RetryAfterSecond, 10))
		}
		handler.writeError(w, dialect, http.StatusTooManyRequests, "rate_limit_exceeded", denial.Reason)
		return
	}
	handler.writeError(w, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Request could not be admitted")
}
func (handler *Handler) writeError(w http.ResponseWriter, dialect string, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	switch dialect {
	case "anthropic":
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": code, "message": message}})
	case "gemini":
		googleStatus := code
		if !strings.Contains(code, "_") {
			googleStatus = strings.ToUpper(code)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": message, "status": googleStatus}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}})
	}
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func nativeAdapter(dialect, adapter string) bool {
	return dialect == adapter || (dialect == "openai" && adapter == "openai_compatible")
}
func hasCapability(values []string, scope string) bool {
	wanted := map[string]string{"chat:generate": "chat", "responses:generate": "chat", "embeddings:generate": "embeddings", "tokens:count": "count_tokens"}[scope]
	for _, v := range values {
		if v == wanted || v == strings.ReplaceAll(scope, ":", "_") {
			return true
		}
	}
	return false
}
func maximumOutput(body map[string]json.RawMessage) int64 {
	for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		var value int64
		if json.Unmarshal(body[name], &value) == nil && value > 0 {
			return value
		}
	}
	return 0
}
func jsonCardinality(raw json.RawMessage) int64 {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		return int64(len(items))
	}
	return 1
}
func geminiMaximumOutput(body map[string]any) int64 {
	config, _ := body["generationConfig"].(map[string]any)
	value, _ := config["maxOutputTokens"].(float64)
	return int64(value)
}
func geminiMethods(capabilities []string) []string {
	var out []string
	for _, v := range capabilities {
		switch v {
		case "chat", "generate_content":
			out = append(out, "generateContent")
		case "count_tokens":
			out = append(out, "countTokens")
		case "embeddings":
			out = append(out, "embedContent")
		}
	}
	return out
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
func setProviderCredential(request *http.Request, adapter, credential string) {
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

func parseUsage(dialect string, raw []byte) (*int64, *int64, *int64) {
	var input, output int64
	foundInput, foundOutput := false, false
	for _, object := range responseObjects(raw) {
		var value map[string]any
		if json.Unmarshal(object, &value) != nil {
			continue
		}
		var usageMap map[string]any
		if dialect == "gemini" {
			usageMap, _ = value["usageMetadata"].(map[string]any)
		} else {
			usageMap, _ = value["usage"].(map[string]any)
			if usageMap == nil {
				usageMap, _ = objectMap(value["response"])["usage"].(map[string]any)
			}
		}
		if dialect == "anthropic" && usageMap == nil {
			if message, _ := value["message"].(map[string]any); message != nil {
				usageMap, _ = message["usage"].(map[string]any)
			}
		}
		if usageMap == nil {
			if dialect == "anthropic" {
				if number, ok := integer(value["input_tokens"]); ok {
					input, foundInput, output, foundOutput = number, true, 0, true
				}
			} else if dialect == "gemini" {
				if number, ok := integer(value["totalTokens"]); ok {
					input, foundInput, output, foundOutput = number, true, 0, true
				}
			}
			continue
		}
		var inputName, outputName string
		if dialect == "gemini" {
			inputName, outputName = "promptTokenCount", "candidatesTokenCount"
		} else if dialect == "anthropic" {
			inputName, outputName = "input_tokens", "output_tokens"
		} else {
			inputName, outputName = "prompt_tokens", "completion_tokens"
			if _, ok := usageMap[inputName]; !ok {
				inputName = "input_tokens"
			}
			if _, ok := usageMap[outputName]; !ok {
				outputName = "output_tokens"
			}
		}
		if number, ok := integer(usageMap[inputName]); ok {
			input, foundInput = number, true
		}
		if number, ok := integer(usageMap[outputName]); ok {
			output, foundOutput = number, true
		}
	}
	if !foundInput || !foundOutput {
		if dialect == "openai" && foundInput {
			for _, object := range responseObjects(raw) {
				var value map[string]any
				_ = json.Unmarshal(object, &value)
				usageMap, _ := value["usage"].(map[string]any)
				if total, ok := integer(usageMap["total_tokens"]); ok && total >= input {
					output, foundOutput = total-input, true
				}
			}
		}
	}
	if !foundInput || !foundOutput {
		return nil, nil, nil
	}
	return &input, &output, nil
}

func countRequestTools(dialect string, raw []byte) int64 {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	if dialect != "gemini" {
		return int64(len(array(value["tools"])))
	}
	var count int64
	for _, group := range array(value["tools"]) {
		count += int64(len(array(objectMap(group)["functionDeclarations"])))
	}
	return count
}

func parseToolMetadata(dialect string, raw []byte) (int64, string) {
	keys := map[string]bool{}
	completed := len(raw) > 0 && bytes.TrimSpace(raw)[0] == '{'
	for _, encoded := range responseObjects(raw) {
		var value map[string]any
		if json.Unmarshal(encoded, &value) != nil {
			continue
		}
		if count, ok := integer(value["_gateway_tool_call_count"]); ok {
			status := stringValue(value["_gateway_tool_call_status"])
			return count, status
		}
		switch dialect {
		case "openai", "openai_compatible":
			for choiceIndex, item := range array(value["choices"]) {
				choice := objectMap(item)
				for callIndex, callValue := range array(objectMap(choice["message"])["tool_calls"]) {
					call := objectMap(callValue)
					keys[firstString(call, "id")+":"+strconv.Itoa(choiceIndex)+":"+strconv.Itoa(callIndex)] = true
				}
				for _, callValue := range array(objectMap(choice["delta"])["tool_calls"]) {
					call := objectMap(callValue)
					keys[strconv.Itoa(choiceIndex)+":"+strconv.FormatInt(number(call["index"]), 10)] = true
				}
			}
			completed = completed || bytes.Contains(raw, []byte("data: [DONE]"))
		case "anthropic":
			for index, partValue := range array(value["content"]) {
				part := objectMap(partValue)
				if stringValue(part["type"]) == "tool_use" {
					keys[firstString(part, "id")+":"+strconv.Itoa(index)] = true
				}
			}
			if stringValue(value["type"]) == "content_block_start" && stringValue(objectMap(value["content_block"])["type"]) == "tool_use" {
				keys[strconv.FormatInt(number(value["index"]), 10)] = true
			}
			completed = completed || stringValue(value["type"]) == "message_stop"
		case "gemini":
			for candidateIndex, candidateValue := range array(value["candidates"]) {
				candidate := objectMap(candidateValue)
				for partIndex, partValue := range array(objectMap(candidate["content"])["parts"]) {
					if len(objectMap(objectMap(partValue)["functionCall"])) > 0 {
						keys[strconv.Itoa(candidateIndex)+":"+strconv.Itoa(partIndex)] = true
					}
				}
				completed = completed || stringValue(candidate["finishReason"]) != ""
			}
		case "responses":
			items := array(value["output"])
			if len(items) == 0 {
				items = array(objectMap(value["response"])["output"])
			}
			if item := objectMap(value["item"]); len(item) > 0 {
				items = append(items, item)
			}
			for index, itemValue := range items {
				item := objectMap(itemValue)
				if stringValue(item["type"]) == "function_call" {
					key := firstString(item, "id", "call_id")
					if key == "" {
						if outputIndex, ok := integer(value["output_index"]); ok {
							key = "index:" + strconv.FormatInt(outputIndex, 10)
						} else {
							key = "item:" + strconv.Itoa(index)
						}
					}
					keys[key] = true
				}
			}
			completed = completed || stringValue(value["type"]) == "response.completed" || stringValue(objectMap(value["response"])["status"]) == "completed"
		}
	}
	if len(keys) == 0 {
		return 0, "none"
	}
	if completed {
		return int64(len(keys)), "completed"
	}
	return int64(len(keys)), "incomplete"
}

func integer(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number > 9_007_199_254_740_991 || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}

func responseObjects(raw []byte) [][]byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return [][]byte{trimmed}
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var data bytes.Buffer
	var objects [][]byte
	flush := func() {
		value := bytes.TrimSpace(data.Bytes())
		if len(value) > 0 && !bytes.Equal(value, []byte("[DONE]")) {
			objects = append(objects, append([]byte(nil), value...))
		}
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			flush()
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	flush()
	return objects
}
