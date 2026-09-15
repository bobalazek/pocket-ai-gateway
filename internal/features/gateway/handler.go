package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const maxInferenceBody = 16 << 20

type Handler struct {
	database  *sql.DB
	keys      *keys.Service
	providers *providers.Service
	usage     *usage.Service
	wake      chan struct{}
	epoch     string
	activeMu  sync.Mutex
	active    map[string]context.CancelFunc
	lastOwner string
	lastKey   string
}

func New(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service) *Handler {
	epoch, err := credentials.RandomToken(12)
	if err != nil {
		epoch = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return &Handler{database: database, keys: keyService, providers: providerService, usage: usageService, wake: make(chan struct{}, 1), epoch: epoch, active: map[string]context.CancelFunc{}}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/openai/v1/models", handler.openAIModels)
	mux.HandleFunc("GET /api/openai/v1/models/{model}", handler.openAIModel)
	mux.HandleFunc("POST /api/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "chat:generate", "chat/completions", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/responses", handler.responses)
	mux.HandleFunc("POST /api/openai/v1/responses/compact", handler.compactResponse)
	mux.HandleFunc("POST /api/openai/v1/conversations", handler.createConversation)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}", handler.getConversation)
	mux.HandleFunc("POST /api/openai/v1/conversations/{conversation_id}", handler.updateConversation)
	mux.HandleFunc("DELETE /api/openai/v1/conversations/{conversation_id}", handler.deleteConversation)
	mux.HandleFunc("POST /api/openai/v1/conversations/{conversation_id}/items", handler.createConversationItems)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}/items", handler.listConversationItems)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}/items/{item_id}", handler.getConversationItem)
	mux.HandleFunc("DELETE /api/openai/v1/conversations/{conversation_id}/items/{item_id}", handler.deleteConversationItem)
	mux.HandleFunc("GET /api/openai/v1/responses/{response_id}", handler.getResponse)
	mux.HandleFunc("POST /api/openai/v1/responses/{response_id}/cancel", handler.cancelResponse)
	mux.HandleFunc("GET /api/openai/v1/responses/{response_id}/input_items", handler.responseInputItems)
	mux.HandleFunc("DELETE /api/openai/v1/responses/{response_id}", handler.deleteResponse)
	mux.HandleFunc("POST /api/openai/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "embeddings:generate", "embeddings", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/moderations", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "moderations:classify", "moderations", "", nil)
	})
	mux.HandleFunc("GET /api/anthropic/v1/models", handler.anthropicModels)
	mux.HandleFunc("GET /api/anthropic/v1/models/{model}", handler.anthropicModel)
	mux.HandleFunc("POST /api/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "chat:generate", "messages", "", nil)
	})
	mux.HandleFunc("POST /api/anthropic/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "tokens:count", "messages/count_tokens", "", nil)
	})
	mux.HandleFunc("GET /api/gemini/v1beta/models", handler.geminiModels)
	mux.HandleFunc("GET /api/gemini/v1beta/models/{model}", handler.geminiModel)
	mux.HandleFunc("POST /api/gemini/v1beta/models/{action...}", handler.geminiAction)
}

func (handler *Handler) forward(response http.ResponseWriter, request *http.Request, dialect, scope, upstreamPath, publicIDOverride string, streamOverride *bool) {
	principal, ok := handler.authenticate(response, request, dialect)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, dialect, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	handler.forwardAuthorized(response, request, dialect, scope, upstreamPath, publicIDOverride, streamOverride, principal, body)
}

func (handler *Handler) forwardAuthorized(response http.ResponseWriter, request *http.Request, dialect, scope, upstreamPath, publicIDOverride string, streamOverride *bool, principal keys.Principal, body []byte) {
	clientOperation := upstreamPath
	recordDialect := dialect
	if recordDialect == "responses_compact" {
		recordDialect = "responses"
	}
	var err error
	originalBodyBytes := int64(len(body))
	requestToolCount := countRequestTools(dialect, body)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var publicID string
	if publicIDOverride != "" {
		publicID = publicIDOverride
	} else if raw := envelope["model"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &publicID)
	}
	if publicID == "" {
		handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	storeResponse := false
	var attached *conversationAttachment
	switch attachment := request.Context().Value(conversationAttachmentContextKey{}).(type) {
	case conversationAttachment:
		if attachment.id != "" {
			attached = &attachment
		}
	case *conversationAttachment:
		if attachment != nil && attachment.id != "" {
			attached = attachment
		}
	}
	if dialect == "responses" {
		if storeResponse, err = validateResponses(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", err.Error())
			return
		}
	}
	if upstreamPath == "moderations" {
		if err = validateModeration(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	stream := false
	_ = json.Unmarshal(envelope["stream"], &stream)
	if streamOverride != nil {
		stream = *streamOverride
	}
	inputEstimate := originalBodyBytes
	outputEstimate := maximumOutput(envelope)
	batchItems := int64(0)
	if upstreamPath == "embeddings" || upstreamPath == "moderations" {
		batchItems = jsonCardinality(envelope["input"])
	} else if dialect == "gemini" {
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		outputEstimate = geminiMaximumOutput(object)
		if upstreamPath == "batchEmbedContents" {
			if values, ok := object["requests"].([]any); ok {
				batchItems = int64(len(values))
			}
		}
	}
	outputBounded := outputEstimate > 0
	if dialect == "responses_compact" {
		outputEstimate, outputBounded = inputEstimate, true
	}
	generation := scope == "chat:generate" || scope == "responses:generate"
	if generation && outputEstimate == 0 {
		outputEstimate = 4096
	}
	translationBody := body
	if dialect == "responses" && storeResponse {
		envelope["store"] = []byte("false")
		translationBody, _ = json.Marshal(envelope)
	}
	if dialect == "gemini" && stream {
		envelope["stream"] = []byte("true")
		translationBody, _ = json.Marshal(envelope)
	}
	translationSupport := map[string]bool{}
	translationChecked := map[string]bool{}
	translationOperation := map[string]string{}
	seed := sha256.Sum256(append([]byte(principal.KeyID+"\x00"+publicID+"\x00"+strconv.FormatInt(time.Now().UnixNano(), 10)+"\x00"), body...))
	plan, err := handler.providers.Route(request.Context(), publicID, providers.RouteOptions{Operation: clientOperation, Streaming: stream, EstimatedInputTokens: inputEstimate, EstimatedOutputTokens: outputEstimate, Seed: string(seed[:]), AllowsConnection: func(connectionID string) bool { return principal.Allows(scope, publicID, connectionID) }, Eligibility: func(target providers.Target) (bool, string) {
		native := nativeTarget(dialect, target.Adapter)
		if !hasCapability(target.Capabilities, scope) {
			return false, "unsupported_capability"
		}
		if !native && scope != "chat:generate" && scope != "responses:generate" {
			return false, "translation_unsupported"
		}
		if dialect == "anthropic" && stream && !native {
			return false, "anthropic_stream_usage_unavailable"
		}
		if !native {
			if !translationChecked[target.Adapter] {
				targetPath, _, err := translateRequest(dialect, target.Adapter, translationBody, target.UpstreamID)
				translationChecked[target.Adapter], translationSupport[target.Adapter] = true, err == nil
				translationOperation[target.Adapter] = targetPath
			}
			if !translationSupport[target.Adapter] {
				return false, "request_translation_unsupported"
			}
		}
		targetOperation := upstreamPath
		if native && dialect == "gemini" {
			targetOperation = clientOperation
		} else if !native {
			targetOperation = translationOperation[target.Adapter]
		}
		if dialect == "responses_compact" && target.Preset != "openai" {
			return false, "preset_operation_unsupported"
		}
		if !providers.PresetSupports(target.Preset, targetOperation) {
			return false, "preset_operation_unsupported"
		}
		return true, ""
	}})
	if err != nil {
		handler.writeError(response, dialect, http.StatusNotFound, "model_not_found", "Model is unavailable")
		return
	}
	overallTimeout := time.Duration(0)
	for _, candidate := range plan.Targets {
		overallTimeout += time.Duration(candidate.Target().TimeoutMS) * time.Millisecond
	}
	overallTimeout = min(max(overallTimeout, time.Second), 10*time.Minute)
	sharedContext, cancelShared := context.WithTimeout(request.Context(), overallTimeout)
	defer cancelShared()
	request = request.Clone(sharedContext)
	rejected, _ := json.Marshal(plan.Rejected)
	requestID := ""
	for index, routeTarget := range plan.Targets {
		target := routeTarget.Target()
		native := nativeTarget(dialect, target.Adapter)
		targetPath, targetBody := upstreamPath, body
		var targetEnvelope map[string]json.RawMessage
		_ = json.Unmarshal(body, &targetEnvelope)
		if native && dialect == "gemini" {
			targetPath = "models/" + url.PathEscape(target.UpstreamID) + ":" + clientOperation
			if stream {
				targetPath += "?alt=sse"
			}
		} else if native {
			targetEnvelope["model"], _ = json.Marshal(target.UpstreamID)
			if dialect == "responses" && storeResponse {
				targetEnvelope["store"] = []byte("false")
			}
			targetBody, err = json.Marshal(targetEnvelope)
		} else {
			targetPath, targetBody, err = translateRequest(dialect, target.Adapter, translationBody, target.UpstreamID)
		}
		if err != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", err.Error())
			return
		}
		admission, admitErr := handler.usage.Admit(request.Context(), usage.AdmissionInput{RequestID: requestID, KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: clientOperation, TargetOperation: targetPath, Scope: scope, Dialect: recordDialect, TargetDialect: target.Adapter, TranslationApplied: !native, RequestToolCount: requestToolCount, SelectionReason: plan.SelectionReason, RejectedCandidatesJSON: string(rejected), RequiredPriceVersionID: routeTarget.PriceVersionID(), RequireFreePrice: plan.FreeOnly, BodyBytes: originalBodyBytes, BatchItems: batchItems, EstimatedInputTokens: inputEstimate, EstimatedOutputTokens: outputEstimate, EnforceOutputBound: generation, OutputBounded: !generation || outputBounded})
		if admitErr != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			handler.writeAdmissionError(response, dialect, admitErr)
			return
		}
		requestID = admission.RequestID
		releaseDispatch, current := handler.providers.BeginDispatch(request.Context(), target)
		if !current {
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Provider configuration changed before dispatch")
			return
		}
		if err = handler.usage.MarkDispatching(request.Context(), admission.AttemptID); err != nil {
			releaseDispatch()
			_ = handler.usage.CancelBeforeDispatch(context.WithoutCancel(request.Context()), admission.AttemptID)
			handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Request could not be dispatched")
			return
		}
		bufferedStream, bufferLimit := stream && attached != nil, int64(0)
		if bufferedStream {
			bufferLimit = maxInferenceBody
		}
		attemptWriter := newAttemptWriter(response, stream && !bufferedStream, bufferLimit)
		started := time.Now()
		var result int
		var raw []byte
		var copyErr error
		if native {
			result, raw, copyErr = handler.dispatch(attemptWriter, request, target, targetPath, targetBody, stream, dialect, releaseDispatch)
		} else {
			result, raw, copyErr = handler.dispatchTranslated(attemptWriter, request, target, targetPath, targetBody, dialect, publicID, stream, releaseDispatch)
		}
		if native && upstreamPath == "moderations" && copyErr == nil && result >= 200 && result < 300 {
			raw, copyErr = rewriteResponseModel(raw, publicID)
			if copyErr == nil {
				attemptWriter.body.Reset()
				_, copyErr = attemptWriter.Write(raw)
			}
		}
		dispatchErr := copyErr
		accountDialect := recordDialect
		if !native {
			accountDialect = target.Adapter
			if accountDialect == "openai_compatible" {
				accountDialect = "openai"
			}
		}
		accountRaw := raw
		if native && bufferedStream {
			accountRaw = attemptWriter.body.Bytes()
		}
		inputTokens, outputTokens, cost := parseUsage(accountDialect, accountRaw)
		toolCalls, toolStatus := parseToolMetadata(accountDialect, accountRaw)
		success := copyErr == nil && result >= 200 && result < 300
		estimatedUsage := false
		if success && upstreamPath == "moderations" && inputTokens == nil && outputTokens == nil {
			input, zero := inputEstimate, int64(0)
			inputTokens, outputTokens, estimatedUsage = &input, &zero, true
		}
		terminalStreamFailure := false
		if success && attached != nil {
			var attachedBody []byte
			if stream {
				var terminalSuccess bool
				attachedBody, terminalSuccess, copyErr = responseStreamWithConversation(attemptWriter.body.Bytes(), attached)
				terminalStreamFailure = copyErr == nil && !terminalSuccess
			} else {
				attachedBody, copyErr = responseWithConversation(attemptWriter.body.Bytes(), attached)
			}
			if copyErr == nil {
				attemptWriter.body.Reset()
				_, _ = attemptWriter.body.Write(attachedBody)
				success = !terminalStreamFailure
			} else {
				success = false
				attemptWriter.Reset()
			}
		}
		var storedResponse preparedResponse
		if success && storeResponse {
			storedResponse, copyErr = prepareStoredResponse(publicID, attemptWriter.body.Bytes())
			if copyErr != nil {
				success = false
				attemptWriter.Reset()
			}
		}
		if request.Context().Err() == nil && (success || copyErr != nil || retryableResult(result, nil)) {
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, clientOperation, stream, success, attemptWriter.FirstByte(started), time.Since(started))
		}
		retryableDispatchError := native && dispatchErr != nil || errors.Is(dispatchErr, errUpstreamResponseInterrupted)
		retry := !terminalStreamFailure && index+1 < len(plan.Targets) && (retryableDispatchError || retryableResult(result, copyErr)) && !attemptWriter.Committed()
		state, status := "succeeded", "provider_reported"
		if estimatedUsage {
			status = "estimated"
		} else if inputTokens == nil || outputTokens == nil {
			status, cost = "unknown", nil
		}
		if scope == "tokens:count" && inputTokens != nil {
			zero := int64(0)
			cost = &zero
		}
		if !success {
			state = "failed"
			if !terminalStreamFailure {
				status = "unknown"
				inputTokens, outputTokens, cost = nil, nil, nil
				if result >= 400 && result < 500 && copyErr == nil {
					zero := int64(0)
					status, inputTokens, outputTokens, cost = "estimated", &zero, &zero, &zero
				}
			}
		}
		if copyErr != nil && toolCalls > 0 {
			toolStatus = "incomplete"
		}
		var storageErr error
		deferredAttachment := attached != nil && attached.deferStore
		if success && (storeResponse || attached != nil) && !deferredAttachment {
			storageContext, cancelStorage := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
			if storeResponse {
				requestBody := body
				if attached != nil {
					requestBody = attached.requestBody
				}
				storageErr = handler.storeResponse(storageContext, principal.OwnerUserID, principal.KeyID, publicID, requestBody, storedResponse, attached)
			} else {
				storageErr = handler.storeConversationTurn(storageContext, *attached)
			}
			cancelStorage()
			if storageErr == nil && storeResponse {
				attemptWriter.body.Reset()
				_, _ = attemptWriter.body.Write(storedResponse.body)
			}
		}
		if errors.Is(request.Context().Err(), context.Canceled) {
			state, status = "interrupted_unknown", "unknown"
			retry = false
		}
		settlement := usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: inputTokens, OutputTokens: outputTokens, CostNanos: cost, ResponseToolCallCount: toolCalls, ToolCallStatus: toolStatus, FinalRequest: !retry && storageErr == nil && !(deferredAttachment && success && request.Context().Err() == nil)}
		settlementErr := handler.settle(admission.AttemptID, settlement)
		if observer, ok := response.(interface {
			observeSettlement(string, string, usage.SettlementInput, error)
		}); ok {
			for settlementErr != nil && !permanentSettlementError(settlementErr) && request.Context().Err() == nil && waitBackground(request.Context(), time.Second) {
				settlementErr = handler.settle(admission.AttemptID, settlement)
			}
			observer.observeSettlement(requestID, admission.AttemptID, settlement, settlementErr)
		}
		if storageErr != nil {
			finalizeContext, cancelFinalize := context.WithTimeout(context.Background(), 2*time.Second)
			_ = handler.usage.FinalizeRequest(finalizeContext, requestID, "failed")
			cancelFinalize()
			attemptWriter.Reset()
			if errors.Is(storageErr, errStoredResponseLimit) {
				handler.writeError(attemptWriter, dialect, http.StatusTooManyRequests, "rate_limit_exceeded", "Stored Response retention limit reached")
			} else if errors.Is(storageErr, errConversationLimit) || errors.Is(storageErr, errConversationItemLimit) {
				handler.writeError(attemptWriter, dialect, http.StatusTooManyRequests, "rate_limit_exceeded", "Conversation retention limit reached")
			} else if errors.Is(storageErr, errConversationChanged) {
				handler.writeError(attemptWriter, dialect, http.StatusConflict, "conflict", "Conversation changed before the Response completed")
			} else {
				handler.writeError(attemptWriter, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be stored")
			}
			attemptWriter.Commit()
			return
		}
		if retry {
			if !waitRetry(request.Context(), index) {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
				return
			}
			continue
		}
		if dispatchErr != nil && result >= 200 && result < 300 && !attemptWriter.Committed() {
			attemptWriter.Reset()
		}
		if !success && attemptWriter.status == 0 {
			handler.writeError(attemptWriter, dialect, http.StatusBadGateway, "upstream_error", "Provider request failed")
		}
		attemptWriter.Commit()
		return
	}
}

func nativeTarget(dialect, adapter string) bool {
	return nativeAdapter(dialect, adapter) || (dialect == "responses" || dialect == "responses_compact") && (adapter == "openai" || adapter == "openai_compatible")
}
func retryableResult(status int, err error) bool {
	if status >= 200 && status < 300 {
		return false
	}
	return err != nil || status == 0 || status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500
}
func waitRetry(ctx context.Context, index int) bool {
	delay := time.Duration(25+(index*17)%51) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

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
	for name, values := range writer.header {
		writer.destination.Header()[name] = append([]string(nil), values...)
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
			if kind, _ := tool["type"].(string); kind != "function" {
				return false, errors.New("only function tools are supported by Responses")
			}
		}
	}
	return stored, nil
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
	stream := name == "streamGenerateContent"
	handler.forward(response, request, "gemini", scope, name, publicID, &stream)
}

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
	_, err = io.Copy(flushWriter{writer: response, flusher: flusher}, io.TeeReader(result.Body, capture))
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
		return handler.translateStream(response, result.Body, dialect, target.Adapter, publicModel)
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
	case "openai", "responses", "responses_compact":
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
		if modelVisibleInDialect(dialect, item) && handler.providers.HasAvailableRouteTarget(request.Context(), item.ID, func(connectionID string) bool { return principal.Allows("models:read", item.ID, connectionID) }) {
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
		return hasCapability(model.Capabilities, "embeddings:generate") || hasCapability(model.Capabilities, "moderations:classify")
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
func writeNativeUpstreamError(w http.ResponseWriter, dialect string, status int, raw []byte) bool {
	openAI := dialect == "openai" || dialect == "responses" || dialect == "responses_compact"
	clientStatus := status == http.StatusBadRequest || status == http.StatusConflict || status == http.StatusUnprocessableEntity || status == http.StatusTooManyRequests
	if !openAI || !clientStatus {
		return false
	}
	var envelope struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	safe := map[string]any{}
	for _, name := range []string{"message", "type", "code", "param"} {
		value, exists := envelope.Error[name]
		if !exists {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			safe[name] = nil
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil || len(text) > 4096 {
			return false
		}
		safe[name] = text
	}
	if message, _ := safe["message"].(string); message == "" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": safe})
	return true
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
	wanted := map[string]string{"chat:generate": "chat", "responses:generate": "chat", "embeddings:generate": "embeddings", "tokens:count": "count_tokens", "moderations:classify": "moderations"}[scope]
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
	scanner.Buffer(make([]byte, 4096), maxInferenceBody+1)
	var data bytes.Buffer
	var objects [][]byte
	limitReached := false
	flush := func() {
		value := bytes.TrimSpace(data.Bytes())
		if len(value) > 0 && !bytes.Equal(value, []byte("[DONE]")) {
			if len(objects) >= maxConversationStreamFrames {
				limitReached = true
				return
			}
			objects = append(objects, append([]byte(nil), value...))
		}
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			flush()
			if limitReached {
				break
			}
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
