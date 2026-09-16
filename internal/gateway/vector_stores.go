package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

type vectorStoreExpiry struct {
	Anchor string `json:"anchor"`
	Days   int64  `json:"days"`
}

type vectorStoreFileCounts struct {
	InProgress int64 `json:"in_progress"`
	Completed  int64 `json:"completed"`
	Failed     int64 `json:"failed"`
	Cancelled  int64 `json:"cancelled"`
	Total      int64 `json:"total"`
}

type vectorStore struct {
	ID           string                `json:"id"`
	Object       string                `json:"object"`
	CreatedAt    int64                 `json:"created_at"`
	Name         string                `json:"name"`
	Status       string                `json:"status"`
	UsageBytes   int64                 `json:"usage_bytes"`
	FileCounts   vectorStoreFileCounts `json:"file_counts"`
	LastActiveAt *int64                `json:"last_active_at"`
	Metadata     map[string]any        `json:"metadata"`
	ExpiresAfter *vectorStoreExpiry    `json:"expires_after,omitempty"`
	ExpiresAt    *int64                `json:"expires_at,omitempty"`
}

var (
	errVectorStoreBodyTooLarge  = errors.New("request body exceeds 64 KiB")
	errVectorStoreFileIngestion = errors.New("vector store file ingestion is not supported")
)

func (handler *Handler) vectorStorePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "openai")
	if ok && !principalHasScope(principal.Scopes, "vector_stores:manage") {
		handler.writeError(response, "openai", http.StatusForbidden, "permission_denied", "Vector Stores access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) createVectorStore(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	body, err := readJSONBody(response, request)
	if err != nil {
		writeVectorStoreBodyError(handler, response, err)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil || !onlyJSONFields(body, "name", "description", "metadata", "expires_after", "file_ids", "chunking_strategy") {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	name, err := optionalVectorStoreText(fields, "name", 256, false)
	description, descriptionErr := optionalVectorStoreText(fields, "description", 4096, false)
	if err == nil {
		err = descriptionErr
	}
	metadata, metadataErr := conversationMetadata(fields["metadata"])
	if err == nil {
		err = metadataErr
	}
	expires, expiryErr := parseVectorStoreExpiry(fields["expires_after"], false)
	if err == nil {
		err = expiryErr
	}
	if err == nil {
		err = validateVectorStoreFiles(fields)
	}
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errVectorStoreFileIngestion) {
			code = "unsupported_feature"
		}
		handler.writeError(response, "openai", http.StatusBadRequest, code, err.Error())
		return
	}
	metadataJSON, _ := json.Marshal(metadata)
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store could not be created")
		return
	}
	now := time.Now().UnixMilli()
	var days, expiresAt any
	if expires != nil {
		days, expiresAt = expires.Days, now+expires.Days*int64(24*time.Hour/time.Millisecond)
	}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(name)+len(description)+len(metadataJSON)))
	}
	id := "vs_" + token
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_vector_stores(id,owner_user_id,key_id,name,description,metadata_json,created_at,last_active_at,expires_after_days,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, principal.OwnerUserID, principal.KeyID, name, description, metadataJSON, now, now, days, expiresAt)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference resource retention limit reached")
			return
		}
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store could not be created")
		return
	}
	item, err := handler.readVectorStore(request.Context(), principal.KeyID, id, false)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store is unavailable")
		return
	}
	writeJSON(response, item)
}

func readJSONBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	reader := http.MaxBytesReader(response, request.Body, 64<<10)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, errVectorStoreBodyTooLarge
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	return body, nil
}

func writeVectorStoreBodyError(handler *Handler, response http.ResponseWriter, err error) {
	if errors.Is(err, errVectorStoreBodyTooLarge) {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
}

func optionalVectorStoreText(fields map[string]json.RawMessage, name string, maximum int, nullable bool) (string, error) {
	raw, exists := fields[name]
	if !exists {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if nullable {
			return "", nil
		}
		return "", errors.New(name + " must be a string")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len([]byte(value)) > maximum {
		return "", errors.New(name + " must be a valid string of at most " + strconv.Itoa(maximum) + " bytes")
	}
	return value, nil
}

func parseVectorStoreExpiry(raw json.RawMessage, nullable bool) (*vectorStoreExpiry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if nullable {
			return nil, nil
		}
		return nil, errors.New("expires_after must be an object")
	}
	var value vectorStoreExpiry
	if json.Unmarshal(raw, &value) != nil || !onlyJSONFields(raw, "anchor", "days") || value.Anchor != "last_active_at" || value.Days < 1 || value.Days > 365 {
		return nil, errors.New("expires_after requires anchor last_active_at and days between 1 and 365")
	}
	return &value, nil
}

func validateVectorStoreFiles(fields map[string]json.RawMessage) error {
	if _, exists := fields["chunking_strategy"]; exists {
		return fmt.Errorf("%w: chunking_strategy requires file ingestion", errVectorStoreFileIngestion)
	}
	if raw, exists := fields["file_ids"]; exists {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("file_ids must be an array")
		}
		var ids []string
		if json.Unmarshal(raw, &ids) != nil {
			return errors.New("file_ids must be an array")
		}
		if len(ids) > 0 {
			return fmt.Errorf("%w: non-empty file_ids require file ingestion", errVectorStoreFileIngestion)
		}
	}
	return nil
}

func (handler *Handler) getVectorStore(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	item, err := handler.readVectorStore(request.Context(), principal.KeyID, request.PathValue("vector_store_id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store is unavailable")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) updateVectorStore(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	body, err := readJSONBody(response, request)
	if err != nil {
		writeVectorStoreBodyError(handler, response, err)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) == 0 || !onlyJSONFields(body, "name", "metadata", "expires_after") {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must contain name, metadata, or expires_after")
		return
	}
	name, nameErr := optionalVectorStoreText(fields, "name", 256, true)
	metadata, metadataErr := conversationMetadata(fields["metadata"])
	expires, expiryErr := parseVectorStoreExpiry(fields["expires_after"], true)
	if nameErr != nil || metadataErr != nil || expiryErr != nil {
		for _, candidate := range []error{nameErr, metadataErr, expiryErr} {
			if candidate != nil {
				err = candidate
				break
			}
		}
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id, now := request.PathValue("vector_store_id"), time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var currentName string
		var currentMetadata []byte
		var lastActive int64
		var currentDays, currentExpiry sql.NullInt64
		err = tx.QueryRowContext(request.Context(), `SELECT name,metadata_json,last_active_at,expires_after_days,expires_at FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, id, principal.KeyID, now).Scan(&currentName, &currentMetadata, &lastActive, &currentDays, &currentExpiry)
		if err == nil {
			if _, exists := fields["name"]; !exists {
				name = currentName
			}
			metadataJSON := currentMetadata
			if _, exists := fields["metadata"]; exists {
				metadataJSON, _ = json.Marshal(metadata)
			}
			var days, expiresAt any
			if _, exists := fields["expires_after"]; !exists {
				if currentDays.Valid {
					days, expiresAt = currentDays.Int64, currentExpiry.Int64
				}
			} else if expires != nil {
				days, expiresAt = expires.Days, lastActive+expires.Days*int64(24*time.Hour/time.Millisecond)
			}
			growth := int64(len(name) + len(metadataJSON) - len(currentName) - len(currentMetadata))
			if growth > 0 {
				err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 0, growth)
			}
			if err == nil {
				_, err = tx.ExecContext(request.Context(), `UPDATE openai_vector_stores SET name=?,metadata_json=?,expires_after_days=?,expires_at=? WHERE id=? AND key_id=?`, name, metadataJSON, days, expiresAt, id, principal.KeyID)
			}
		}
	}
	var item vectorStore
	if err == nil {
		item, err = readVectorStoreFrom(request.Context(), tx, principal.KeyID, id, true)
	}
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference resource retention limit reached")
			return
		}
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store could not be updated")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) deleteVectorStore(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("vector_store_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "object": "vector_store.deleted", "deleted": true})
}

func (handler *Handler) listVectorStores(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	limit, order, err := pageOptions(request, "desc")
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query := request.URL.Query()
	for _, name := range []string{"after", "before"} {
		if len(query[name]) > 1 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", name+" must be specified once")
			return
		}
	}
	after, before := query.Get("after"), query.Get("before")
	if after != "" && before != "" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after and before cannot be combined")
		return
	}
	predicate := `key_id=? AND (expires_at IS NULL OR expires_at>?)`
	arguments := []any{principal.KeyID, time.Now().UnixMilli()}
	cursor, comparison := after, ">"
	if order == "DESC" {
		comparison = "<"
	}
	if before != "" {
		cursor = before
		if comparison == ">" {
			comparison = "<"
		} else {
			comparison = ">"
		}
	}
	if cursor != "" {
		var createdAt int64
		err = handler.database.QueryRowContext(request.Context(), `SELECT created_at FROM openai_vector_stores WHERE `+predicate+` AND id=?`, principal.KeyID, time.Now().UnixMilli(), cursor).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "cursor is not a valid Vector Store cursor")
			return
		}
		if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Stores are unavailable")
			return
		}
		predicate += ` AND (created_at ` + comparison + ` ? OR (created_at=? AND id ` + comparison + ` ?))`
		arguments = append(arguments, createdAt, createdAt, cursor)
	}
	arguments = append(arguments, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), vectorStoreSelect+` WHERE `+predicate+` ORDER BY created_at `+order+`,id `+order+` LIMIT ?`, arguments...)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Stores are unavailable")
		return
	}
	defer rows.Close()
	items := make([]vectorStore, 0, limit)
	more := false
	for rows.Next() {
		item, scanErr := scanVectorStore(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Stores are unavailable")
		return
	}
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": nil, "last_id": nil}
	if len(items) > 0 {
		result["first_id"], result["last_id"] = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(response, result)
}

const vectorStoreSelect = `SELECT id,name,metadata_json,created_at,last_active_at,expires_after_days,expires_at FROM openai_vector_stores`

type vectorStoreScanner interface{ Scan(...any) error }

func (handler *Handler) readVectorStore(ctx context.Context, keyID, id string, includeExpired bool) (vectorStore, error) {
	return readVectorStoreFrom(ctx, handler.database, keyID, id, includeExpired)
}

func readVectorStoreFrom(ctx context.Context, query responseQueryer, keyID, id string, includeExpired bool) (vectorStore, error) {
	predicate := ` WHERE id=? AND key_id=?`
	arguments := []any{id, keyID}
	if !includeExpired {
		predicate += ` AND (expires_at IS NULL OR expires_at>?)`
		arguments = append(arguments, time.Now().UnixMilli())
	}
	return scanVectorStore(query.QueryRowContext(ctx, vectorStoreSelect+predicate, arguments...))
}

func scanVectorStore(scanner vectorStoreScanner) (vectorStore, error) {
	var item vectorStore
	var metadata []byte
	var createdAt, lastActive int64
	var days, expiresAt sql.NullInt64
	err := scanner.Scan(&item.ID, &item.Name, &metadata, &createdAt, &lastActive, &days, &expiresAt)
	if err != nil {
		return item, err
	}
	if json.Unmarshal(metadata, &item.Metadata) != nil {
		return item, errors.New("invalid Vector Store metadata")
	}
	item.Object, item.Status, item.CreatedAt = "vector_store", "completed", createdAt/1000
	activeSeconds := lastActive / 1000
	item.LastActiveAt = &activeSeconds
	if days.Valid {
		item.ExpiresAfter = &vectorStoreExpiry{Anchor: "last_active_at", Days: days.Int64}
	}
	if expiresAt.Valid {
		seconds := expiresAt.Int64 / 1000
		item.ExpiresAt = &seconds
		if expiresAt.Int64 <= time.Now().UnixMilli() {
			item.Status = "expired"
		}
	}
	return item, nil
}
