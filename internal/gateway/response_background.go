package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const (
	backgroundQueueJobs  = 32
	backgroundQueueBytes = 64 << 20
	backgroundOwnerJobs  = 16
	backgroundOwnerBytes = 32 << 20
	backgroundKeyJobs    = 4
	backgroundKeyBytes   = 16 << 20
)

var errBackgroundStateChanged = errors.New("background response state changed")

type backgroundJob struct {
	id, ownerID, keyID, modelID string
	request                     []byte
	createdAt                   time.Time
	attachment                  *conversationAttachment
	invalidAttachment           bool
}

func (handler *Handler) enqueueResponse(response http.ResponseWriter, request *http.Request, principal keys.Principal, envelope map[string]json.RawMessage, body []byte, attachment *conversationAttachment) {
	if !principalHasScope(principal.Scopes, "responses:generate") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		return
	}
	var modelID string
	_ = json.Unmarshal(envelope["model"], &modelID)
	if modelID == "" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var stream bool
	if raw, exists := envelope["stream"]; exists && json.Unmarshal(raw, &stream) != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "stream must be a boolean")
		return
	}
	stored := true
	if raw, exists := envelope["store"]; exists && json.Unmarshal(raw, &stored) != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "store must be a boolean")
		return
	}
	if stream || !stored {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "background Responses require store=true and stream=false")
		return
	}
	check := make(map[string]json.RawMessage, len(envelope))
	for name, raw := range envelope {
		check[name] = raw
	}
	check["background"], check["store"] = json.RawMessage(`false`), json.RawMessage(`false`)
	if _, err := validateResponses(check); err != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	webSearch, _ := validateResponseWebSearch(check)
	if webSearch.enabled && !principalHasScope(principal.Scopes, "responses:web_search") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Web search access is not permitted")
		return
	}
	if !handler.providers.HasAvailableRouteTarget(request.Context(), modelID, func(connectionID string) bool {
		return principal.Allows("responses:generate", modelID, connectionID)
	}, func(target providers.Target) bool {
		if !webSearch.enabled {
			return true
		}
		eligible, _ := webSearchTargetEligibility(target)
		return eligible
	}) {
		handler.writeError(response, "responses", http.StatusNotFound, "model_not_found", "Model is unavailable")
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be queued")
		return
	}
	id, now := "resp_"+token, time.Now()
	conversationID := ""
	if attachment != nil {
		conversationID = attachment.id
	}
	queued := responseState(id, modelID, "queued", now, nil, conversationID)
	requestBody := body
	var storedConversationID any
	var conversationRevision any
	var conversationItems []byte
	if attachment != nil {
		requestBody = attachment.requestBody
		storedConversationID = attachment.id
		conversationRevision = attachment.revision
		conversationItems, _ = json.Marshal(attachment.newItems)
	}
	queueLimited := false
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		incoming := int64(len(requestBody) + len(conversationItems) + len(queued))
		err = checkBackgroundQueueCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, incoming)
		queueLimited = errors.Is(err, errBackgroundQueueLimit)
		if err == nil {
			err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, incoming+maxInferenceBody)
			queueLimited = errors.Is(err, errRetainedResourceLimit)
		}
		if err == nil {
			_, err = tx.ExecContext(request.Context(), `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json,conversation_id,conversation_revision,conversation_items_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, principal.OwnerUserID, principal.KeyID, modelID, queued, now.UnixMilli(), now.Add(storedResponseLifetime).UnixMilli(), "queued", requestBody, storedConversationID, conversationRevision, conversationItems)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	if err != nil {
		if queueLimited {
			handler.writeError(response, "responses", http.StatusTooManyRequests, "rate_limit_exceeded", "Background response queue limit reached")
			return
		}
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "queue_full", "Background response queue is unavailable")
		return
	}
	select {
	case handler.wake <- struct{}{}:
	default:
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(queued)
}

func (handler *Handler) RunBackground(ctx context.Context) {
	for {
		if err := handler.recoverBackground(ctx); err == nil && handler.recoverMessageBatches(ctx) == nil && handler.recoverOpenAIBatches(ctx) == nil {
			break
		}
		if !waitBackground(ctx, time.Second) {
			return
		}
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		ran := false
		for offset := range 3 {
			kind := (handler.nextBackground + offset) % 3
			if handler.runQueuedWork(ctx, kind) {
				handler.nextBackground = (kind + 1) % 3
				ran = true
				break
			}
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-handler.wake:
		case <-ticker.C:
		}
	}
}

func (handler *Handler) runQueuedWork(ctx context.Context, kind int) bool {
	switch kind {
	case 0:
		job, ok := handler.claimBackground(ctx)
		if ok {
			handler.runBackground(ctx, job)
		}
		return ok
	case 1:
		job, ok := handler.claimMessageBatch(ctx)
		if ok {
			handler.runMessageBatch(ctx, job)
		}
		return ok
	default:
		job, ok := handler.claimOpenAIBatch(ctx)
		if ok {
			handler.runOpenAIBatch(ctx, job)
		}
		return ok
	}
}

func (handler *Handler) recoverBackground(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT id,model_id,created_at,conversation_id FROM stored_responses WHERE state='running' AND expires_at>?`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	type interrupted struct {
		id, model    string
		created      int64
		conversation sql.NullString
	}
	var values []interrupted
	for rows.Next() {
		var value interrupted
		if rows.Scan(&value.id, &value.model, &value.created, &value.conversation) == nil {
			values = append(values, value)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, value := range values {
		body := responseState(value.id, value.model, "failed", time.UnixMilli(value.created), map[string]string{"code": "background_interrupted", "message": "The gateway restarted while the provider outcome was unknown."}, value.conversation.String)
		result, err := handler.database.ExecContext(ctx, `UPDATE stored_responses SET state='interrupted_unknown',body_json=?,finished_at=?,conversation_items_json=NULL WHERE id=? AND state='running'`, body, time.Now().UnixMilli(), value.id)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.New("background recovery state changed")
		}
	}
	return nil
}

func (handler *Handler) claimBackground(ctx context.Context) (backgroundJob, bool) {
	var job backgroundJob
	var created int64
	var conversationID sql.NullString
	var conversationRevision sql.NullInt64
	var conversationItems []byte
	now := time.Now().UnixMilli()
	err := handler.database.QueryRowContext(ctx, `UPDATE stored_responses SET state='running',lease_epoch=?,claimed_at=? WHERE id=(SELECT id FROM stored_responses WHERE state='queued' AND expires_at>? ORDER BY CASE WHEN owner_user_id=? THEN 1 ELSE 0 END,CASE WHEN key_id=? THEN 1 ELSE 0 END,created_at,id LIMIT 1) AND state='queued' RETURNING id,owner_user_id,key_id,model_id,request_json,created_at,conversation_id,conversation_revision,conversation_items_json`, handler.epoch, now, now, handler.lastOwner, handler.lastKey).Scan(&job.id, &job.ownerID, &job.keyID, &job.modelID, &job.request, &created, &conversationID, &conversationRevision, &conversationItems)
	if err != nil {
		return backgroundJob{}, false
	}
	job.createdAt = time.UnixMilli(created)
	if conversationID.Valid || conversationRevision.Valid || len(conversationItems) > 0 {
		var items []json.RawMessage
		job.invalidAttachment = !conversationID.Valid || !conversationRevision.Valid || json.Unmarshal(conversationItems, &items) != nil
		if !job.invalidAttachment {
			job.attachment = &conversationAttachment{id: conversationID.String, keyID: job.keyID, ownerID: job.ownerID, revision: conversationRevision.Int64, newItems: items, requestBody: job.request}
		}
	}
	stateConversationID := ""
	if job.attachment != nil {
		stateConversationID = job.attachment.id
	}
	body := responseState(job.id, job.modelID, "in_progress", job.createdAt, nil, stateConversationID)
	if !handler.transitionBackground(ctx, `UPDATE stored_responses SET body_json=? WHERE id=? AND state='running' AND lease_epoch=?`, body, job.id, handler.epoch) {
		return backgroundJob{}, false
	}
	handler.lastOwner, handler.lastKey = job.ownerID, job.keyID
	return job, true
}

func (handler *Handler) runBackground(parent context.Context, job backgroundJob) {
	if job.invalidAttachment {
		handler.failBackground(parent, job, "invalid_request", "The stored conversation attachment is invalid.")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(job.request, &envelope) != nil || envelope == nil {
		handler.failBackground(parent, job, "invalid_request", "The stored request is invalid.")
		return
	}
	webSearch, validateErr := validateResponseWebSearch(envelope)
	if validateErr != nil {
		handler.failBackground(parent, job, "invalid_request", "The stored request is invalid.")
		return
	}
	principal, err := handler.keys.Principal(parent, job.keyID)
	if err != nil || !principalHasScope(principal.Scopes, "responses:generate") {
		handler.failBackground(parent, job, "permission_denied", "The API key or its grants are no longer active.")
		return
	}
	if webSearch.enabled && !principalHasScope(principal.Scopes, "responses:web_search") {
		handler.failBackground(parent, job, "permission_denied", "Web search access is no longer permitted.")
		return
	}
	if job.attachment != nil {
		var revision int64
		err = handler.database.QueryRowContext(parent, "SELECT revision FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", job.attachment.id, job.keyID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) || err == nil && revision != job.attachment.revision {
			handler.failBackground(parent, job, "conversation_conflict", "The conversation changed before the Response started.")
			return
		}
		if err != nil {
			handler.failBackground(parent, job, "gateway_unavailable", "The conversation could not be checked.")
			return
		}
	}
	envelope["background"], envelope["store"], envelope["stream"] = json.RawMessage(`false`), json.RawMessage(`false`), json.RawMessage(`false`)
	body, _ := json.Marshal(envelope)
	if job.attachment != nil {
		body, err = conversationDispatchBody(body)
		if err != nil {
			handler.failBackground(parent, job, "invalid_request", "The stored conversation attachment is invalid.")
			return
		}
	}
	jobContext, cancel := context.WithCancel(parent)
	handler.activeMu.Lock()
	handler.active[job.id] = cancel
	handler.activeMu.Unlock()
	defer func() {
		cancel()
		handler.activeMu.Lock()
		delete(handler.active, job.id)
		handler.activeMu.Unlock()
	}()
	var state string
	if handler.database.QueryRowContext(jobContext, `SELECT state FROM stored_responses WHERE id=?`, job.id).Scan(&state) != nil || state != "running" {
		return
	}
	recorder := &memoryResponse{header: make(http.Header)}
	request, _ := http.NewRequestWithContext(jobContext, http.MethodPost, "/api/openai/v1/responses", bytes.NewReader(body))
	if job.attachment != nil {
		job.attachment.deferStore = true
		request = request.WithContext(context.WithValue(request.Context(), conversationAttachmentContextKey{}, job.attachment))
	}
	handler.forwardAuthorized(recorder, request, "responses", "responses:generate", "responses", "", nil, principal, body)
	if parent.Err() != nil {
		return
	}
	for recorder.attemptID != "" && recorder.settlementErr != nil && !permanentSettlementError(recorder.settlementErr) {
		recorder.settlementErr = handler.settle(recorder.attemptID, recorder.settlement)
		if recorder.settlementErr != nil && !waitBackground(parent, time.Second) {
			return
		}
	}
	if recorder.settlementErr != nil {
		handler.failBackground(parent, job, "accounting_failed", "The provider result could not be reconciled with gateway accounting.")
		return
	}
	if job.attachment != nil {
		if recorder.requestID == "" || !handler.transitionBackground(parent, `UPDATE stored_responses SET request_id=? WHERE id=? AND state='running' AND lease_epoch=? AND (request_id IS NULL OR request_id=?)`, recorder.requestID, job.id, handler.epoch, recorder.requestID) {
			handler.failBackground(parent, job, "accounting_failed", "The provider result could not be linked to gateway accounting.")
			return
		}
	}
	if recorder.status >= 200 && recorder.status < 300 {
		if job.attachment != nil && recorder.settlement.FinalRequest {
			handler.failBackground(parent, job, "background_interrupted", "The provider request was interrupted before conversation completion.")
			return
		}
		result := recorder.body.Bytes()
		storedState := "completed"
		var terminalErr error
		if webSearch.enabled {
			parsedWebSearch, parseErr := parseWebSearchResponse(result)
			terminalErr = parseErr
			if parseErr == nil && (parsedWebSearch.status == "failed" || parsedWebSearch.status == "cancelled") {
				storedState = parsedWebSearch.status
			}
		}
		stored, rewriteErr := rewriteResponse(job.id, job.modelID, true, job.createdAt, result)
		if terminalErr != nil {
			rewriteErr = terminalErr
		}
		if rewriteErr == nil {
			if job.attachment == nil {
				handler.transitionBackground(parent, `UPDATE stored_responses SET state=?,body_json=?,finished_at=? WHERE id=? AND state='running' AND lease_epoch=?`, storedState, stored, time.Now().UnixMilli(), job.id, handler.epoch)
				return
			}
			for {
				err = handler.completeBackgroundConversation(parent, job, stored, recorder.requestID)
				if err == nil || errors.Is(err, errBackgroundStateChanged) {
					if errors.Is(err, errBackgroundStateChanged) && !recorder.settlement.FinalRequest {
						handler.finalizeBackgroundRequest(parent, recorder.requestID, "failed")
					}
					return
				}
				if errors.Is(err, usage.ErrConflict) {
					handler.failBackground(parent, job, "accounting_failed", "The background request was finalized before conversation completion.")
					return
				}
				if errors.Is(err, errConversationChanged) {
					if !recorder.settlement.FinalRequest {
						handler.finalizeBackgroundRequest(parent, recorder.requestID, "failed")
					}
					handler.failBackground(parent, job, "conversation_conflict", "The conversation changed before the Response completed.")
					return
				}
				if errors.Is(err, errConversationLimit) || errors.Is(err, errConversationItemLimit) {
					if !recorder.settlement.FinalRequest {
						handler.finalizeBackgroundRequest(parent, recorder.requestID, "failed")
					}
					handler.failBackground(parent, job, "conversation_limit", "The conversation retention limit was reached.")
					return
				}
				if !waitBackground(parent, 250*time.Millisecond) {
					return
				}
			}
		}
	}
	if recorder.requestID != "" && !recorder.settlement.FinalRequest {
		handler.finalizeBackgroundRequest(parent, recorder.requestID, "failed")
	}
	handler.failBackground(parent, job, "background_failed", "The provider request failed.")
}

func (handler *Handler) completeBackgroundConversation(ctx context.Context, job backgroundJob, body []byte, requestID string) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendConversationTurn(ctx, tx, *job.attachment, time.Now().UnixMilli()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE stored_responses SET state='completed',body_json=?,finished_at=?,conversation_items_json=NULL WHERE id=? AND state='running' AND lease_epoch=?`, body, time.Now().UnixMilli(), job.id, handler.epoch)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errBackgroundStateChanged
	}
	if err := handler.usage.FinalizeRequestTx(ctx, tx, requestID, "succeeded"); err != nil {
		return err
	}
	return tx.Commit()
}

func (handler *Handler) finalizeBackgroundRequest(ctx context.Context, requestID, state string) bool {
	for requestID != "" {
		if err := handler.usage.FinalizeRequest(ctx, requestID, state); err == nil || errors.Is(err, usage.ErrConflict) {
			return true
		}
		if !waitBackground(ctx, 250*time.Millisecond) {
			return false
		}
	}
	return true
}

func (handler *Handler) failBackground(ctx context.Context, job backgroundJob, code, message string) {
	conversationID := ""
	if job.attachment != nil {
		conversationID = job.attachment.id
	}
	body := responseState(job.id, job.modelID, "failed", job.createdAt, map[string]string{"code": code, "message": message}, conversationID)
	handler.transitionBackground(ctx, `UPDATE stored_responses SET state='failed',body_json=?,finished_at=?,conversation_items_json=NULL WHERE id=? AND state IN ('queued','running')`, body, time.Now().UnixMilli(), job.id)
}

func (handler *Handler) transitionBackground(ctx context.Context, statement string, args ...any) bool {
	for {
		result, err := handler.database.ExecContext(ctx, statement, args...)
		if err == nil {
			changed, rowsErr := result.RowsAffected()
			return rowsErr == nil && changed == 1
		}
		if !waitBackground(ctx, 250*time.Millisecond) {
			return false
		}
	}
}

func waitBackground(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (handler *Handler) cancelActive(id string) {
	handler.activeMu.Lock()
	cancel := handler.active[id]
	handler.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type memoryResponse struct {
	header        http.Header
	status        int
	body          bytes.Buffer
	requestID     string
	attemptID     string
	settlement    usage.SettlementInput
	settlementErr error
}

func (response *memoryResponse) observeSettlement(requestID, attemptID string, input usage.SettlementInput, err error) {
	response.requestID, response.attemptID, response.settlement, response.settlementErr = requestID, attemptID, input, err
}

func (response *memoryResponse) Header() http.Header { return response.header }
func (response *memoryResponse) WriteHeader(status int) {
	if response.status == 0 {
		response.status = status
	}
}
func (response *memoryResponse) Write(value []byte) (int, error) {
	if response.status == 0 {
		response.status = http.StatusOK
	}
	return response.body.Write(value)
}
