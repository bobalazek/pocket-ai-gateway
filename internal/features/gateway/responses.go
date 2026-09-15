package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const storedResponseLifetime = 30 * 24 * time.Hour

type preparedResponse struct {
	id        string
	body      []byte
	createdAt time.Time
}

func prepareStoredResponse(modelID string, body []byte) (preparedResponse, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return preparedResponse{}, errors.New("provider response is not a JSON object")
	}
	var object, status string
	if json.Unmarshal(value["object"], &object) != nil || object != "response" || json.Unmarshal(value["status"], &status) != nil || status == "" {
		return preparedResponse{}, errors.New("provider response is not a valid Response object")
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		return preparedResponse{}, err
	}
	now := time.Now()
	id := "resp_" + token
	value["id"], _ = json.Marshal(id)
	value["model"], _ = json.Marshal(modelID)
	value["object"] = json.RawMessage(`"response"`)
	value["store"] = json.RawMessage(`true`)
	value["background"] = json.RawMessage(`false`)
	if _, ok := value["created_at"]; !ok {
		value["created_at"] = json.RawMessage(strconv.FormatInt(now.Unix(), 10))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return preparedResponse{}, err
	}
	return preparedResponse{id: id, body: encoded, createdAt: now}, nil
}

func (handler *Handler) storeResponse(ctx context.Context, ownerID, keyID, modelID string, value preparedResponse) error {
	_, err := handler.database.ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`, value.id, ownerID, keyID, modelID, value.body, value.createdAt.UnixMilli(), value.createdAt.Add(storedResponseLifetime).UnixMilli())
	return err
}

func (handler *Handler) getResponse(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(response, request, "responses")
	if !ok || !principalHasScope(principal.Scopes, "responses:generate") {
		if ok {
			handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		}
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
	principal, ok := handler.authenticate(response, request, "responses")
	if !ok || !principalHasScope(principal.Scopes, "responses:generate") {
		if ok {
			handler.writeError(response, "responses", http.StatusForbidden, "permission_denied", "Responses access is not permitted")
		}
		return
	}
	id := request.PathValue("response_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM stored_responses WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "responses", http.StatusServiceUnavailable, "gateway_unavailable", "Response could not be deleted")
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		handler.writeError(response, "responses", http.StatusNotFound, "not_found", "Response not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "object": "response", "deleted": true})
}

func principalHasScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}
