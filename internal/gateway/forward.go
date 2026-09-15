package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/protocol"
)

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
	if dialect != "responses" && !(dialect == "anthropic" && upstreamPath == "messages") && containsHostedWebSearchTool(envelope["tools"]) {
		handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", "web search is supported only by POST /api/openai/v1/responses or POST /api/anthropic/v1/messages")
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
	promptCache := promptCacheRequest{}
	anthropicWebSearch := anthropicWebSearchRequest{}
	if dialect == "anthropic" && upstreamPath == "messages" {
		if promptCache, err = validatePromptCache(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if anthropicWebSearch, err = validateAnthropicWebSearch(envelope); err != nil {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		if anthropicWebSearch.enabled && promptCache.enabled {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request_error", "prompt caching cannot be combined with web search")
			return
		}
		if anthropicWebSearch.enabled && !principalHasScope(principal.Scopes, "messages:web_search") {
			handler.writeError(response, dialect, http.StatusForbidden, "permission_error", "Web search access is not permitted")
			return
		}
	}
	storeResponse := false
	webSearch := responseWebSearchRequest{}
	storedChat, _ := request.Context().Value(storedChatContextKey{}).(*storedChatRequest)
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
		webSearch, _ = validateResponseWebSearch(envelope)
		if webSearch.enabled && !principalHasScope(principal.Scopes, "responses:web_search") {
			handler.writeError(response, dialect, http.StatusForbidden, "permission_denied", "Web search access is not permitted")
			return
		}
	}
	hostedWebSearch := webSearch.enabled || anthropicWebSearch.enabled
	webSearchMaxCalls := webSearch.maxCalls
	if anthropicWebSearch.enabled {
		webSearchMaxCalls = anthropicWebSearch.maxUses
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
	if hostedWebSearch {
		if webSearchMaxCalls > (int64(^uint64(0)>>1)-inputEstimate)/webSearchInputPerCall {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request", "web search input reservation exceeds the supported range")
			return
		}
		inputEstimate += webSearchInputPerCall * webSearchMaxCalls
	}
	outputEstimate := maximumOutput(envelope)
	batchItems := int64(0)
	if messageBatchSize, ok := request.Context().Value(messageBatchItemContextKey{}).(int64); ok {
		batchItems = messageBatchSize
	}
	openAIBatchItems, openAIBatch := request.Context().Value(openAIBatchItemContextKey{}).(int64)
	if openAIBatch {
		batchItems = openAIBatchItems
	}
	imageGeneration := upstreamPath == "images/generations"
	imageEdit := upstreamPath == "images/edits"
	imageVariation := upstreamPath == "images/variations"
	speechGeneration := upstreamPath == "audio/speech"
	audioTranscription := upstreamPath == "audio/transcriptions"
	audioTranslation := upstreamPath == "audio/translations"
	imageOperation := imageGeneration || imageEdit || imageVariation
	opaqueMedia := imageOperation || speechGeneration || audioTranscription || audioTranslation
	if upstreamPath == "embeddings" || upstreamPath == "moderations" {
		batchItems = jsonCardinality(envelope["input"])
	} else if opaqueMedia {
		batchItems = 1
		if imageOperation {
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
	generation := scope == "chat:generate" || scope == "responses:generate" || scope == "images:generate" || scope == "images:edit" || scope == "images:variation" || scope == "audio:speech" || scope == "audio:transcribe" || scope == "audio:translate"
	if generation && outputEstimate == 0 && !opaqueMedia {
		outputEstimate = 4096
	}
	translationBody := body
	if dialect == "responses" && storeResponse {
		envelope["store"] = []byte("false")
		translationBody, _ = json.Marshal(envelope)
	} else if storedChat != nil {
		envelope["store"] = []byte("false")
		delete(envelope, "metadata")
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
		if webSearch.enabled {
			if eligible, reason := webSearchTargetEligibility(target); !eligible {
				return false, reason
			}
		}
		if anthropicWebSearch.enabled {
			if eligible, reason := anthropicWebSearchTargetEligibility(target); !eligible {
				return false, reason
			}
		}
		if promptCache.enabled {
			if !native || target.Preset != "anthropic" {
				return false, "prompt_cache_native_required"
			}
			if !slices.Contains(target.Capabilities, "prompt_cache") || !slices.Contains(target.UpstreamCapabilities, "prompt_cache") {
				return false, "unsupported_capability"
			}
			if target.RoutingStrategy == "lowest_cost" || target.FreeOnly {
				return false, "cache_price_contract_unavailable"
			}
		}
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
				targetPath, _, err := protocol.TranslateRequest(dialect, target.Adapter, translationBody, target.UpstreamID)
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
			} else if storedChat != nil {
				targetEnvelope["store"] = []byte("false")
				delete(targetEnvelope, "metadata")
			}
			targetBody, err = json.Marshal(targetEnvelope)
		} else {
			targetPath, targetBody, err = protocol.TranslateRequest(dialect, target.Adapter, translationBody, target.UpstreamID)
		}
		if err != nil {
			if requestID != "" {
				_ = handler.usage.CloseFailedRequest(context.WithoutCancel(request.Context()), requestID)
			}
			handler.writeError(response, dialect, http.StatusBadRequest, "unsupported_feature", err.Error())
			return
		}
		admission, admitErr := handler.usage.Admit(request.Context(), usage.AdmissionInput{RequestID: requestID, KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: publicID, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: clientOperation, TargetOperation: targetPath, Scope: scope, Dialect: recordDialect, TargetDialect: target.Adapter, TranslationApplied: !native, RequestToolCount: requestToolCount, WebSearchMaxCalls: webSearchMaxCalls, SelectionReason: plan.SelectionReason, RejectedCandidatesJSON: string(rejected), RequiredPriceVersionID: routeTarget.PriceVersionID(), RequireFreePrice: plan.FreeOnly, PriceUnavailable: promptCache.enabled || hostedWebSearch, BodyBytes: originalBodyBytes, BatchItems: batchItems, EstimatedInputTokens: inputEstimate, EstimatedOutputTokens: outputEstimate, EnforceOutputBound: generation, OutputBounded: !generation || outputBounded})
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
			result, raw, copyErr = handler.dispatch(attemptWriter, request, target, targetPath, targetBody, stream, dialect, anthropicWebSearch.enabled && stream, releaseDispatch)
		} else {
			result, raw, copyErr = handler.dispatchTranslated(attemptWriter, request, target, targetPath, targetBody, dialect, publicID, stream, releaseDispatch)
		}
		if openAIBatch && copyErr == nil && result >= 200 && result < 300 && !json.Valid(attemptWriter.body.Bytes()) {
			copyErr = errors.New("provider returned invalid JSON")
			semanticResponseError = true
			attemptWriter.Reset()
		}
		if native && upstreamPath == "moderations" && copyErr == nil && result >= 200 && result < 300 {
			raw, copyErr = rewriteResponseModel(raw, publicID)
			semanticResponseError = copyErr != nil
			if copyErr == nil {
				attemptWriter.body.Reset()
				_, copyErr = attemptWriter.Write(raw)
			}
		}
		if webSearch.enabled && copyErr == nil && result >= 200 && result < 300 {
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
		var webSearchCallCount *int64
		anthropicWebSearchUsageKnown := true
		webSearchTerminalFailure := false
		webSearchResponseStatus := ""
		if webSearch.enabled && copyErr == nil && result >= 200 && result < 300 {
			var parsedWebSearch responseWebSearchResult
			parsedWebSearch, copyErr = parseWebSearchResponse(raw)
			webSearchResponseStatus = parsedWebSearch.status
			webSearchTerminalFailure = copyErr == nil && (parsedWebSearch.status == "failed" || parsedWebSearch.status == "cancelled")
			if copyErr == nil && !webSearchTerminalFailure && parsedWebSearch.completedCalls > webSearch.maxCalls {
				copyErr = errors.New("provider exceeded max_tool_calls")
			}
			semanticResponseError = copyErr != nil
			if copyErr == nil && !webSearchTerminalFailure {
				webSearchCallCount = &parsedWebSearch.completedCalls
			} else {
				if copyErr != nil {
					attemptWriter.Reset()
				}
			}
		}
		if anthropicWebSearch.enabled && copyErr == nil && result >= 200 && result < 300 {
			if stream {
				webSearchCallCount, copyErr = parseAnthropicWebSearchStream(raw, anthropicWebSearch.maxUses)
				anthropicWebSearchUsageKnown = copyErr == nil
			} else {
				var exceeded bool
				webSearchCallCount, anthropicWebSearchUsageKnown, exceeded = parseAnthropicWebSearchUsage(raw, anthropicWebSearch.maxUses)
				if exceeded {
					copyErr = errors.New("provider exceeded max_uses")
				}
			}
			if copyErr != nil {
				semanticResponseError = true
				if !attemptWriter.Committed() {
					attemptWriter.Reset()
				}
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
		parsed := parseUsageDetails(accountDialect, accountRaw)
		inputTokens, outputTokens, cost := parsed.inputTokens, parsed.outputTokens, (*int64)(nil)
		cacheCreationInputTokens, cacheReadInputTokens := parsed.cacheCreationInputTokens, parsed.cacheReadInputTokens
		cacheCreation5mTokens, cacheCreation1hTokens := parsed.cacheCreation5mTokens, parsed.cacheCreation1hTokens
		if countedInputTokens != nil {
			zero := int64(0)
			inputTokens, outputTokens = countedInputTokens, &zero
		}
		toolCalls, toolStatus := parseToolMetadata(accountDialect, accountRaw)
		if webSearchCallCount != nil {
			toolCalls += *webSearchCallCount
			if toolStatus == "none" && *webSearchCallCount > 0 {
				toolStatus = "completed"
			}
		}
		success := copyErr == nil && result >= 200 && result < 300 && !webSearchTerminalFailure
		if success && promptCache.enabled && (cacheCreationInputTokens == nil || cacheReadInputTokens == nil) {
			inputTokens, outputTokens, cost = nil, nil, nil
			cacheCreationInputTokens, cacheReadInputTokens = nil, nil
			cacheCreation5mTokens, cacheCreation1hTokens = nil, nil
		}
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
		if (success || webSearchTerminalFailure) && storeResponse {
			storedResponse, copyErr = prepareStoredResponse(publicID, attemptWriter.body.Bytes())
			if copyErr != nil {
				success = false
				attemptWriter.Reset()
			} else if webSearchTerminalFailure {
				storedResponse.state = webSearchResponseStatus
			}
		}
		var storedChatCompletion preparedChatCompletion
		if success && storedChat != nil {
			storedChatCompletion, copyErr = prepareStoredChatCompletion(publicID, attemptWriter.body.Bytes(), storedChat.metadata)
			if copyErr != nil {
				success = false
				attemptWriter.Reset()
			}
		}
		if request.Context().Err() == nil && (success || webSearchTerminalFailure || copyErr != nil || retryableResult(result, nil)) {
			_ = handler.providers.RecordRouteOutcome(context.WithoutCancel(request.Context()), target, clientOperation, stream, success, attemptWriter.FirstByte(started), time.Since(started))
		}
		retryableDispatchError := !semanticResponseError && (native && dispatchErr != nil || errors.Is(dispatchErr, errUpstreamResponseInterrupted))
		retry := !openAIBatch && !opaqueMedia && !promptCache.enabled && !hostedWebSearch && !terminalStreamFailure && index+1 < len(plan.Targets) && (retryableDispatchError || retryableResult(result, copyErr)) && !attemptWriter.Committed()
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
		if hostedWebSearch && (status == "unknown" || !anthropicWebSearchUsageKnown) {
			status = "unknown"
			inputTokens, outputTokens, cost, webSearchCallCount = nil, nil, nil, nil
			cacheCreationInputTokens, cacheReadInputTokens = nil, nil
			cacheCreation5mTokens, cacheCreation1hTokens = nil, nil
			toolCalls, toolStatus = 0, "none"
		}
		if !success {
			webSearchCallCount = nil
			if hostedWebSearch {
				toolCalls, toolStatus = 0, "none"
			}
			state = "failed"
			if !terminalStreamFailure {
				status = "unknown"
				inputTokens, outputTokens, cost = nil, nil, nil
				cacheCreationInputTokens, cacheReadInputTokens = nil, nil
				cacheCreation5mTokens, cacheCreation1hTokens = nil, nil
				if !hostedWebSearch && result >= 400 && result < 500 && copyErr == nil {
					zero := int64(0)
					status, inputTokens, outputTokens, cost = "estimated", &zero, &zero, &zero
				}
			}
		}
		if promptCache.enabled && !success {
			status = "unknown"
			inputTokens, outputTokens, cost = nil, nil, nil
			cacheCreationInputTokens, cacheReadInputTokens = nil, nil
			cacheCreation5mTokens, cacheCreation1hTokens = nil, nil
		}
		if copyErr != nil && toolCalls > 0 {
			toolStatus = "incomplete"
		}
		var storageErr error
		deferredAttachment := attached != nil && attached.deferStore
		if success && attached != nil && !storeResponse && !deferredAttachment {
			storageContext, cancelStorage := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
			storageErr = handler.storeConversationTurn(storageContext, *attached)
			cancelStorage()
		}
		if request.Context().Err() != nil {
			state, status = "interrupted_unknown", "unknown"
			if hostedWebSearch {
				inputTokens, outputTokens, cost, webSearchCallCount = nil, nil, nil, nil
				cacheCreationInputTokens, cacheReadInputTokens = nil, nil
				cacheCreation5mTokens, cacheCreation1hTokens = nil, nil
				toolCalls, toolStatus = 0, "none"
			}
			retry = false
		}
		publishAfterSettlement := !deferredAttachment && (success && state == "succeeded" && (storeResponse || storedChat != nil) || webSearchTerminalFailure && state == "failed" && storeResponse)
		settlement := usage.SettlementInput{IdempotencyKey: "dispatch:" + admission.AttemptID, State: state, UsageStatus: status, InputTokens: inputTokens, OutputTokens: outputTokens, CacheCreationInputTokens: cacheCreationInputTokens, CacheReadInputTokens: cacheReadInputTokens, CacheCreation5mTokens: cacheCreation5mTokens, CacheCreation1hTokens: cacheCreation1hTokens, WebSearchCallCount: webSearchCallCount, CostNanos: cost, ResponseToolCallCount: toolCalls, ToolCallStatus: toolStatus, FinalRequest: !retry && storageErr == nil && !publishAfterSettlement && !(deferredAttachment && success && request.Context().Err() == nil)}
		settlementErr := handler.settle(admission.AttemptID, settlement)
		if observer, ok := response.(interface {
			observeSettlement(string, string, usage.SettlementInput, error)
		}); ok {
			for settlementErr != nil && !permanentSettlementError(settlementErr) && request.Context().Err() == nil && waitBackground(request.Context(), time.Second) {
				settlementErr = handler.settle(admission.AttemptID, settlement)
			}
			observer.observeSettlement(requestID, admission.AttemptID, settlement, settlementErr)
		}
		if publishAfterSettlement && settlementErr != nil {
			attemptWriter.Reset()
			handler.writeError(attemptWriter, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Provider usage could not be settled")
			attemptWriter.Commit()
			return
		}
		if publishAfterSettlement {
			storageContext, cancelStorage := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
			if storedChat != nil {
				storageErr = handler.storeChatCompletion(storageContext, requestID, principal, publicID, *storedChat, storedChatCompletion)
			} else {
				requestBody := body
				if attached != nil {
					requestBody = attached.requestBody
				}
				storageErr = handler.storeResponse(storageContext, requestID, principal.OwnerUserID, principal.KeyID, publicID, requestBody, storedResponse, attached, state)
			}
			cancelStorage()
			if storageErr == nil {
				attemptWriter.body.Reset()
				if storedChat != nil {
					_, _ = attemptWriter.body.Write(storedChatCompletion.body)
				} else {
					_, _ = attemptWriter.body.Write(storedResponse.body)
				}
			}
		}
		if storageErr != nil {
			finalizeContext, cancelFinalize := context.WithTimeout(context.Background(), 2*time.Second)
			if finalizeErr := handler.usage.FinalizeFailedRequest(finalizeContext, requestID); finalizeErr != nil {
				storageErr = errors.Join(storageErr, finalizeErr)
			}
			cancelFinalize()
			attemptWriter.Reset()
			if errors.Is(storageErr, errRetainedResourceLimit) {
				handler.writeError(attemptWriter, dialect, http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference result retention limit reached")
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
