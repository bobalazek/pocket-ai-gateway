package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	mux.HandleFunc("POST /api/openai/v1/responses/input_tokens", handler.responseInputTokens)
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
	mux.HandleFunc("POST /api/openai/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "images:generate", "images/generations", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "audio:speech", "audio/speech", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/audio/transcriptions", handler.audioTranscription)
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
	if multipart, ok := multipartRequest(request); ok {
		originalBodyBytes = int64(len(multipart.body))
	}
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
	if upstreamPath == "images/generations" {
		if err = validateImageGeneration(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if upstreamPath == "audio/speech" {
		if err = validateSpeech(envelope); err != nil {
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
	imageGeneration := upstreamPath == "images/generations"
	speechGeneration := upstreamPath == "audio/speech"
	audioTranscription := upstreamPath == "audio/transcriptions"
	opaqueMedia := imageGeneration || speechGeneration || audioTranscription
	if upstreamPath == "embeddings" || upstreamPath == "moderations" {
		batchItems = jsonCardinality(envelope["input"])
	} else if opaqueMedia {
		batchItems = 1
		if imageGeneration {
			_ = json.Unmarshal(envelope["n"], &batchItems)
		}
		outputEstimate = 0
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
	generation := scope == "chat:generate" || scope == "responses:generate" || scope == "images:generate" || scope == "audio:speech" || scope == "audio:transcribe"
	if generation && outputEstimate == 0 && !opaqueMedia {
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
		if !hasCapability(target.Capabilities, scope) || !hasCapability(target.UpstreamCapabilities, scope) {
			return false, "unsupported_capability"
		}
		if opaqueMedia && target.RoutingStrategy == "lowest_cost" {
			return false, "cost_estimate_unavailable"
		}
		if opaqueMedia && target.FreeOnly {
			return false, "free_price_contract_unavailable"
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
		if multipart, ok := multipartRequest(request); native && ok {
			targetBody, err = rewriteMultipartModel(multipart, target.UpstreamID)
		} else if native && dialect == "gemini" {
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
		var countedInputTokens *int64
		semanticResponseError := false
		if native {
			result, raw, copyErr = handler.dispatch(attemptWriter, request, target, targetPath, targetBody, stream, dialect, releaseDispatch)
		} else {
			result, raw, copyErr = handler.dispatchTranslated(attemptWriter, request, target, targetPath, targetBody, dialect, publicID, stream, releaseDispatch)
		}
		if native && upstreamPath == "moderations" && copyErr == nil && result >= 200 && result < 300 {
			raw, copyErr = rewriteResponseModel(raw, publicID)
			semanticResponseError = copyErr != nil
			if copyErr == nil {
				attemptWriter.body.Reset()
				_, copyErr = attemptWriter.Write(raw)
			}
		}
		if native && upstreamPath == "responses/input_tokens" && copyErr == nil && result >= 200 && result < 300 {
			var count int64
			count, copyErr = validateResponseInputTokens(raw)
			semanticResponseError = copyErr != nil
			if copyErr == nil {
				countedInputTokens = &count
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
		if countedInputTokens != nil {
			zero := int64(0)
			inputTokens, outputTokens = countedInputTokens, &zero
		}
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
		retryableDispatchError := !semanticResponseError && (native && dispatchErr != nil || errors.Is(dispatchErr, errUpstreamResponseInterrupted))
		retry := !opaqueMedia && !terminalStreamFailure && index+1 < len(plan.Targets) && (retryableDispatchError || retryableResult(result, copyErr)) && !attemptWriter.Committed()
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
