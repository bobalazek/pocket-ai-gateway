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
	if !nativeAdapter(dialect, target.Adapter) || !hasCapability(target.Capabilities, scope) {
		handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_operation", "Model does not support this native operation")
		return
	}
	if !principal.Allows(scope, publicID, target.TargetConnectionID) {
		handler.writeError(response, dialect, http.StatusNotFound, "model_not_found", "Model is unavailable to this key")
		return
	}
	encodedModel, _ := json.Marshal(target.UpstreamID)
	envelope["model"] = encodedModel
	body, err = json.Marshal(envelope)
	if err != nil {
		handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "Request body is invalid")
		return
	}
	stream := false
	_ = json.Unmarshal(envelope["stream"], &stream)
	inputEstimate := originalBodyBytes
	outputEstimate := maximumOutput(envelope)
	batchItems := int64(0)
	if upstreamPath == "embeddings" {
		batchItems = jsonCardinality(envelope["input"])
	}
	admission, err := handler.usage.Admit(request.Context(), usage.AdmissionInput{KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: upstreamPath, Scope: scope, Dialect: dialect, BodyBytes: originalBodyBytes, BatchItems: batchItems, EstimatedInputTokens: inputEstimate, EstimatedOutputTokens: outputEstimate, EnforceOutputBound: scope == "chat:generate", OutputBounded: scope != "chat:generate" || outputEstimate > 0})
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
	result, raw, copyErr := handler.dispatch(response, request, target, upstreamPath, body, stream, dialect, releaseDispatch)
	state, status := "succeeded", "provider_reported"
	final := true
	inputTokens, outputTokens, cost := parseUsage(dialect, raw)
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
	if errors.Is(request.Context().Err(), context.Canceled) {
		state = "interrupted_unknown"
		status = "unknown"
	}
	handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: inputTokens, OutputTokens: outputTokens, CostNanos: cost, FinalRequest: final})
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
	if err != nil || !nativeAdapter("gemini", target.Adapter) || !hasCapability(target.Capabilities, scope) || !principal.Allows(scope, publicID, target.TargetConnectionID) {
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
	batchItems := int64(0)
	if name == "batchEmbedContents" {
		if values, ok := object["requests"].([]any); ok {
			batchItems = int64(len(values))
		}
	}
	outputEstimate := geminiMaximumOutput(object)
	admission, err := handler.usage.Admit(request.Context(), usage.AdmissionInput{KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: name, Scope: scope, Dialect: "gemini", BodyBytes: int64(len(body)), BatchItems: batchItems, EstimatedInputTokens: int64(len(body)), EstimatedOutputTokens: outputEstimate, EnforceOutputBound: scope == "chat:generate", OutputBounded: scope != "chat:generate" || outputEstimate > 0})
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
	stream := name == "streamGenerateContent"
	relative := "models/" + url.PathEscape(target.UpstreamID) + ":" + name
	if stream {
		relative += "?alt=sse"
	}
	result, raw, copyErr := handler.dispatch(response, request, target, relative, body, stream, "gemini", releaseDispatch)
	state, status := "succeeded", "provider_reported"
	in, out, cost := parseUsage("gemini", raw)
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
	if errors.Is(request.Context().Err(), context.Canceled) {
		state, status = "interrupted_unknown", "unknown"
	}
	handler.settle(admission.AttemptID, usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: in, OutputTokens: out, CostNanos: cost, FinalRequest: true})
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
	case "openai":
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
		if nativeAdapter(dialect, item.Adapter) && principal.Allows("models:read", item.ID, item.TargetConnectionID) {
			filtered = append(filtered, item)
		}
	}
	return principal, filtered, true
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
	wanted := map[string]string{"chat:generate": "chat", "embeddings:generate": "embeddings", "tokens:count": "count_tokens"}[scope]
	for _, v := range values {
		if v == wanted || v == strings.ReplaceAll(scope, ":", "_") {
			return true
		}
	}
	return false
}
func maximumOutput(body map[string]json.RawMessage) int64 {
	for _, name := range []string{"max_tokens", "max_completion_tokens"} {
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
