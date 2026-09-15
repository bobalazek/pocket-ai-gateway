package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const (
	storedResponseLifetime = 30 * 24 * time.Hour
	backgroundQueueJobs    = 32
	backgroundQueueBytes   = 64 << 20
	backgroundOwnerJobs    = 16
	backgroundOwnerBytes   = 32 << 20
	backgroundKeyJobs      = 4
	backgroundKeyBytes     = 16 << 20
	retainedResponseJobs   = 10000
	retainedResponseBytes  = 1 << 30
	retainedOwnerJobs      = 2500
	retainedOwnerBytes     = 512 << 20
	retainedKeyJobs        = 250
	retainedKeyBytes       = 128 << 20
)

var errStoredResponseLimit = errors.New("stored response retention limit reached")

type preparedResponse struct {
	id        string
	body      []byte
	createdAt time.Time
}

type backgroundJob struct {
	id, ownerID, keyID, modelID string
	request                     []byte
	createdAt                   time.Time
}

func (handler *Handler) responses(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "responses")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "responses", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var background bool
	if raw, exists := envelope["background"]; exists && json.Unmarshal(raw, &background) != nil {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "background must be a boolean")
		return
	}
	if !background {
		handler.forwardAuthorized(response, request, "responses", "responses:generate", "responses", "", nil, principal, body)
		return
	}
	handler.enqueueResponse(response, request, principal, envelope, body)
}

func (handler *Handler) compactResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "responses_compact")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody))
	if err != nil {
		handler.writeError(response, "responses_compact", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	var model string
	_ = json.Unmarshal(envelope["model"], &model)
	if model == "" || len(envelope["input"]) == 0 {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "invalid_request", "model and input are required")
		return
	}
	if err := validateCompactInput(envelope["input"]); err != nil {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	if _, exists := envelope["stream"]; exists {
		handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", "stream is not supported by compact")
		return
	}
	for _, field := range []string{"conversation", "previous_response_id"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) {
			handler.writeError(response, "responses_compact", http.StatusBadRequest, "unsupported_feature", field+" is not supported by compact")
			return
		}
	}
	handler.forwardAuthorized(response, request, "responses_compact", "responses:generate", "responses/compact", "", nil, principal, body)
}

func validateCompactInput(raw json.RawMessage) error {
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return errors.New("input must be a string or input-item array")
	}
	if _, ok := input.(string); ok {
		return nil
	}
	items, ok := input.([]any)
	if !ok {
		return errors.New("input must be a string or input-item array")
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return errors.New("input array must contain only input-item objects")
		}
	}
	return rejectCompactReferences(items)
}

func rejectCompactReferences(value any) error {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if err := rejectCompactReferences(item); err != nil {
				return err
			}
		}
	case map[string]any:
		if value["type"] == "item_reference" {
			return errors.New("provider item references are not supported by compact")
		}
		for key, item := range value {
			if (key == "file_id" || key == "container_id" || key == "item_id") && item != nil && item != "" {
				return errors.New("provider resource references are not supported by compact")
			}
			if err := rejectCompactReferences(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (handler *Handler) enqueueResponse(response http.ResponseWriter, request *http.Request, principal keys.Principal, envelope map[string]json.RawMessage, body []byte) {
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
	if !handler.providers.HasAvailableRouteTarget(request.Context(), modelID, func(connectionID string) bool {
		return principal.Allows("responses:generate", modelID, connectionID)
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
	queued := responseState(id, modelID, "queued", now, nil)
	queueLimited := false
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		var count, size, ownerCount, ownerSize, keyCount, keySize int64
		err = tx.QueryRowContext(request.Context(), `SELECT COUNT(*),COALESCE(SUM(length(request_json)+length(body_json)),0),
			COALESCE(SUM(CASE WHEN owner_user_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN length(request_json)+length(body_json) ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN key_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN key_id=? THEN length(request_json)+length(body_json) ELSE 0 END),0)
			FROM stored_responses WHERE state IN ('queued','running')`, principal.OwnerUserID, principal.OwnerUserID, principal.KeyID, principal.KeyID).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
		incoming := int64(len(body) + len(queued))
		if err == nil && (ownerCount >= backgroundOwnerJobs || ownerSize+incoming > backgroundOwnerBytes || keyCount >= backgroundKeyJobs || keySize+incoming > backgroundKeyBytes) {
			queueLimited = true
			err = errors.New("queue limit reached")
		} else if err == nil && (count >= backgroundQueueJobs || size+incoming > backgroundQueueBytes) {
			err = errors.New("queue full")
		}
		if err == nil {
			err = checkRetainedResponseCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, incoming+maxInferenceBody)
			queueLimited = errors.Is(err, errStoredResponseLimit)
		}
		if err == nil {
			_, err = tx.ExecContext(request.Context(), `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json) VALUES(?,?,?,?,?,?,?,?,?)`, id, principal.OwnerUserID, principal.KeyID, modelID, queued, now.UnixMilli(), now.Add(storedResponseLifetime).UnixMilli(), "queued", body)
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

func prepareStoredResponse(modelID string, body []byte) (preparedResponse, error) {
	token, err := credentials.RandomToken(18)
	if err != nil {
		return preparedResponse{}, err
	}
	now := time.Now()
	id := "resp_" + token
	encoded, err := rewriteResponse(id, modelID, false, now, body)
	return preparedResponse{id: id, body: encoded, createdAt: now}, err
}

func rewriteResponse(id, modelID string, background bool, createdAt time.Time, body []byte) ([]byte, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return nil, errors.New("provider response is not a JSON object")
	}
	var object, status string
	if json.Unmarshal(value["object"], &object) != nil || object != "response" || json.Unmarshal(value["status"], &status) != nil || status == "" {
		return nil, errors.New("provider response is not a valid Response object")
	}
	value["id"], _ = json.Marshal(id)
	value["model"], _ = json.Marshal(modelID)
	value["object"] = json.RawMessage(`"response"`)
	value["store"] = json.RawMessage(`true`)
	value["background"] = json.RawMessage(strconv.FormatBool(background))
	if background || len(value["created_at"]) == 0 {
		value["created_at"] = json.RawMessage(strconv.FormatInt(createdAt.Unix(), 10))
	}
	return json.Marshal(value)
}

func (handler *Handler) storeResponse(ctx context.Context, ownerID, keyID, modelID string, requestBody []byte, value preparedResponse) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkRetainedResponseCapacity(ctx, tx, ownerID, keyID, int64(len(requestBody)+len(value.body))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,request_json) VALUES(?,?,?,?,?,?,?,?)`, value.id, ownerID, keyID, modelID, value.body, value.createdAt.UnixMilli(), value.createdAt.Add(storedResponseLifetime).UnixMilli(), requestBody); err != nil {
		return err
	}
	return tx.Commit()
}

type responseQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkRetainedResponseCapacity(ctx context.Context, query responseQueryer, ownerID, keyID string, incoming int64) error {
	var count, size, ownerCount, ownerSize, keyCount, keySize int64
	err := query.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(COALESCE(length(request_json),0)+length(body_json)+CASE WHEN state IN ('queued','running') THEN ? ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN owner_user_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN COALESCE(length(request_json),0)+length(body_json)+CASE WHEN state IN ('queued','running') THEN ? ELSE 0 END ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN key_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN key_id=? THEN COALESCE(length(request_json),0)+length(body_json)+CASE WHEN state IN ('queued','running') THEN ? ELSE 0 END ELSE 0 END),0)
		FROM stored_responses WHERE expires_at>?`, maxInferenceBody, ownerID, ownerID, maxInferenceBody, keyID, keyID, maxInferenceBody, time.Now().UnixMilli()).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
	if err != nil {
		return err
	}
	if count >= retainedResponseJobs || size+incoming > retainedResponseBytes || ownerCount >= retainedOwnerJobs || ownerSize+incoming > retainedOwnerBytes || keyCount >= retainedKeyJobs || keySize+incoming > retainedKeyBytes {
		return errStoredResponseLimit
	}
	return nil
}

func (handler *Handler) getResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	var body []byte
	err := handler.database.QueryRowContext(request.Context(), `SELECT body_json FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, request.PathValue("response_id"), principal.KeyID, time.Now().UnixMilli()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response is unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) deleteResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("response_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	handler.cancelActive(id)
	writeJSON(response, map[string]any{"id": id, "object": "response", "deleted": true})
}

func (handler *Handler) cancelResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id, now := request.PathValue("response_id"), time.Now()
	var modelID, state string
	var createdAt int64
	err := handler.database.QueryRowContext(request.Context(), `SELECT model_id,state,created_at FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, now.UnixMilli()).Scan(&modelID, &state, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response is unavailable")
		return
	}
	if state != "queued" && state != "running" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "Only queued or in-progress Responses can be cancelled")
		return
	}
	body := responseState(id, modelID, "cancelled", time.UnixMilli(createdAt), nil)
	result, err := handler.database.ExecContext(request.Context(), `UPDATE stored_responses SET state='cancelled',body_json=?,cancel_requested=1,finished_at=? WHERE id=? AND key_id=? AND state IN ('queued','running')`, body, now.UnixMilli(), id, principal.KeyID)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be cancelled")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "responses", http.StatusConflict, "conflict", "Response state changed")
		return
	}
	handler.cancelActive(id)
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(body)
}

func (handler *Handler) responseInputItems(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.responsePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("response_id")
	var raw []byte
	err := handler.database.QueryRowContext(request.Context(), `SELECT request_json FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response input is unavailable")
		return
	}
	if len(raw) == 0 {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "Input items are unavailable for Responses created before this gateway version")
		return
	}
	if len(request.URL.Query()["include"]) > 0 || len(request.URL.Query()["include[]"]) > 0 {
		handler.writeError(response, "responses", http.StatusBadRequest, "unsupported_feature", "include projections are not supported for input items")
		return
	}
	items, err := inputItems(id, raw)
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response input is unavailable")
		return
	}
	if order := request.URL.Query().Get("order"); order == "" || order == "desc" {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	} else if order != "asc" {
		handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "order must be asc or desc")
		return
	}
	start := 0
	if after := request.URL.Query().Get("after"); after != "" {
		start = -1
		for index, item := range items {
			var value map[string]json.RawMessage
			_ = json.Unmarshal(item, &value)
			var itemID string
			_ = json.Unmarshal(value["id"], &itemID)
			if itemID == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "after is not a valid input item cursor")
			return
		}
	}
	limit := 20
	if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 100 {
			handler.writeError(response, "responses", http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
	}
	end := min(start+limit, len(items))
	page := items[start:end]
	result := map[string]any{"object": "list", "data": page, "has_more": end < len(items)}
	if len(page) > 0 {
		var first, last map[string]json.RawMessage
		_ = json.Unmarshal(page[0], &first)
		_ = json.Unmarshal(page[len(page)-1], &last)
		var firstID, lastID string
		_ = json.Unmarshal(first["id"], &firstID)
		_ = json.Unmarshal(last["id"], &lastID)
		result["first_id"], result["last_id"] = firstID, lastID
	}
	writeJSON(response, result)
}

func inputItems(responseID string, requestBody []byte) ([]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(requestBody, &envelope) != nil {
		return nil, errors.New("invalid stored input")
	}
	raw := envelope["input"]
	var text string
	var items []json.RawMessage
	if json.Unmarshal(raw, &text) == nil {
		content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": text}})
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": json.RawMessage(content)})
		items = []json.RawMessage{item}
	} else if json.Unmarshal(raw, &items) != nil {
		return nil, errors.New("invalid stored input")
	}
	for index, item := range items {
		var value map[string]json.RawMessage
		if json.Unmarshal(item, &value) != nil {
			return nil, errors.New("invalid stored input item")
		}
		sum := sha256.Sum256([]byte(responseID + ":" + strconv.Itoa(index)))
		value["id"], _ = json.Marshal("item_" + hex.EncodeToString(sum[:12]))
		items[index], _ = json.Marshal(value)
	}
	return items, nil
}

func (handler *Handler) responsePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "responses")
	if ok && !principalHasScope(principal.Scopes, "responses:generate") {
		handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func principalHasScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func responseState(id, modelID, status string, createdAt time.Time, responseError map[string]string) []byte {
	value := map[string]any{
		"id": id, "object": "response", "created_at": createdAt.Unix(), "status": status,
		"background": true, "store": true, "model": modelID, "output": []any{},
		"error": responseError, "incomplete_details": nil, "usage": nil,
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// RunBackground processes durable Responses jobs until ctx is cancelled.
func (handler *Handler) RunBackground(ctx context.Context) {
	for {
		if err := handler.recoverBackground(ctx); err == nil {
			break
		}
		if !waitBackground(ctx, time.Second) {
			return
		}
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		job, ok := handler.claimBackground(ctx)
		if ok {
			handler.runBackground(ctx, job)
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

func (handler *Handler) recoverBackground(ctx context.Context) error {
	rows, err := handler.database.QueryContext(ctx, `SELECT id,model_id,created_at FROM stored_responses WHERE state='running' AND expires_at>?`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	type interrupted struct {
		id, model string
		created   int64
	}
	var values []interrupted
	for rows.Next() {
		var value interrupted
		if rows.Scan(&value.id, &value.model, &value.created) == nil {
			values = append(values, value)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, value := range values {
		body := responseState(value.id, value.model, "failed", time.UnixMilli(value.created), map[string]string{"code": "background_interrupted", "message": "The gateway restarted while the provider outcome was unknown."})
		result, err := handler.database.ExecContext(ctx, `UPDATE stored_responses SET state='interrupted_unknown',body_json=?,finished_at=? WHERE id=? AND state='running'`, body, time.Now().UnixMilli(), value.id)
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
	now := time.Now().UnixMilli()
	err := handler.database.QueryRowContext(ctx, `UPDATE stored_responses SET state='running',lease_epoch=?,claimed_at=? WHERE id=(SELECT id FROM stored_responses WHERE state='queued' AND expires_at>? ORDER BY CASE WHEN owner_user_id=? THEN 1 ELSE 0 END,CASE WHEN key_id=? THEN 1 ELSE 0 END,created_at,id LIMIT 1) AND state='queued' RETURNING id,owner_user_id,key_id,model_id,request_json,created_at`, handler.epoch, now, now, handler.lastOwner, handler.lastKey).Scan(&job.id, &job.ownerID, &job.keyID, &job.modelID, &job.request, &created)
	if err != nil {
		return backgroundJob{}, false
	}
	job.createdAt = time.UnixMilli(created)
	body := responseState(job.id, job.modelID, "in_progress", job.createdAt, nil)
	if !handler.transitionBackground(ctx, `UPDATE stored_responses SET body_json=? WHERE id=? AND state='running' AND lease_epoch=?`, body, job.id, handler.epoch) {
		return backgroundJob{}, false
	}
	handler.lastOwner, handler.lastKey = job.ownerID, job.keyID
	return job, true
}

func (handler *Handler) runBackground(parent context.Context, job backgroundJob) {
	principal, err := handler.keys.Principal(parent, job.keyID)
	if err != nil || !principalHasScope(principal.Scopes, "responses:generate") {
		handler.failBackground(parent, job, "permission_denied", "The API key or its grants are no longer active.")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(job.request, &envelope) != nil {
		handler.failBackground(parent, job, "invalid_request", "The stored request is invalid.")
		return
	}
	envelope["background"], envelope["store"], envelope["stream"] = json.RawMessage(`false`), json.RawMessage(`false`), json.RawMessage(`false`)
	body, _ := json.Marshal(envelope)
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
	if recorder.status >= 200 && recorder.status < 300 {
		stored, rewriteErr := rewriteResponse(job.id, job.modelID, true, job.createdAt, recorder.body.Bytes())
		if rewriteErr == nil {
			handler.transitionBackground(parent, `UPDATE stored_responses SET state='completed',body_json=?,finished_at=? WHERE id=? AND state='running' AND lease_epoch=?`, stored, time.Now().UnixMilli(), job.id, handler.epoch)
			return
		}
	}
	handler.failBackground(parent, job, "background_failed", "The provider request failed.")
}

func (handler *Handler) failBackground(ctx context.Context, job backgroundJob, code, message string) {
	body := responseState(job.id, job.modelID, "failed", job.createdAt, map[string]string{"code": code, "message": message})
	handler.transitionBackground(ctx, `UPDATE stored_responses SET state='failed',body_json=?,finished_at=? WHERE id=? AND state IN ('queued','running')`, body, time.Now().UnixMilli(), job.id)
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
	attemptID     string
	settlement    usage.SettlementInput
	settlementErr error
}

func (response *memoryResponse) observeSettlement(attemptID string, input usage.SettlementInput, err error) {
	response.attemptID, response.settlement, response.settlementErr = attemptID, input, err
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
