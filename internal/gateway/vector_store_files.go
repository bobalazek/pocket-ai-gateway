package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"
	"unicode/utf8"
)

type vectorStoreFile struct {
	ID               string         `json:"id"`
	Object           string         `json:"object"`
	CreatedAt        int64          `json:"created_at"`
	VectorStoreID    string         `json:"vector_store_id"`
	Status           string         `json:"status"`
	LastError        any            `json:"last_error"`
	UsageBytes       int64          `json:"usage_bytes"`
	Attributes       map[string]any `json:"attributes"`
	ChunkingStrategy map[string]any `json:"chunking_strategy"`
}

var (
	errVectorStoreResourceNotFound = errors.New("vector store or file not found")
	errVectorStoreFileAttached     = errors.New("file is already attached to the vector store")
	errVectorStoreStaticChunking   = errors.New("static chunking requires parsed content support")
)

func (handler *Handler) createVectorStoreFile(response http.ResponseWriter, request *http.Request) {
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
	if json.Unmarshal(body, &fields) != nil || fields == nil || !onlyJSONFields(body, "file_id", "attributes", "chunking_strategy") {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must contain file_id")
		return
	}
	var fileID string
	if json.Unmarshal(fields["file_id"], &fileID) != nil || fileID == "" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "file_id must be a non-empty string")
		return
	}
	attributes, err := vectorStoreFileAttributes(fields["attributes"])
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	chunking, err := vectorStoreFileChunking(fields["chunking_strategy"])
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errVectorStoreStaticChunking) {
			code = "unsupported_feature"
		}
		handler.writeError(response, "openai", http.StatusBadRequest, code, err.Error())
		return
	}
	attributesJSON, _ := json.Marshal(attributes)
	storeID, now := request.PathValue("vector_store_id"), time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = attachVectorStoreFile(request.Context(), tx, principal.OwnerUserID, principal.KeyID, storeID, fileID, attributesJSON, chunking, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeVectorStoreFileMutationError(response, err, "attached")
		return
	}
	item, err := handler.readVectorStoreFile(request.Context(), principal.KeyID, storeID, fileID)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file is unavailable")
		return
	}
	writeJSON(response, item)
}

func attachVectorStoreFile(ctx context.Context, tx *sql.Tx, ownerID, keyID, storeID, fileID string, attributes, chunking []byte, now int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, storeID, keyID, now).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errVectorStoreResourceNotFound
		}
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM openai_files WHERE id=? AND key_id=? AND expires_at>?`, fileID, keyID, now).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errVectorStoreResourceNotFound
		}
		return err
	}
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM openai_vector_store_files WHERE vector_store_id=? AND file_id=?`, storeID, fileID).Scan(&exists)
	if err == nil {
		return errVectorStoreFileAttached
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = checkRetainedResourceCapacity(ctx, tx, ownerID, keyID, 1, int64(len(fileID)+len(attributes)+len(chunking))); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_vector_store_files(vector_store_id,file_id,attributes_json,chunking_strategy_json,created_at) VALUES(?,?,?,?,?)`, storeID, fileID, attributes, chunking, now); err != nil {
		return err
	}
	return touchVectorStore(ctx, tx, keyID, storeID, now)
}

func touchVectorStore(ctx context.Context, tx *sql.Tx, keyID, storeID string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE openai_vector_stores SET last_active_at=?,expires_at=CASE WHEN expires_after_days IS NULL THEN NULL ELSE ?+expires_after_days*86400000 END WHERE id=? AND key_id=?`, now, now, storeID, keyID)
	return err
}

func vectorStoreFileAttributes(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var attributes map[string]any
	if json.Unmarshal(raw, &attributes) != nil || attributes == nil || len(attributes) > 16 {
		return nil, errors.New("attributes must contain at most 16 scalar values")
	}
	for key, value := range attributes {
		if utf8.RuneCountInString(key) > 64 {
			return nil, errors.New("attribute keys must be at most 64 characters")
		}
		switch value := value.(type) {
		case string:
			if utf8.RuneCountInString(value) > 512 {
				return nil, errors.New("string attributes must be at most 512 characters")
			}
		case float64:
			if math.IsInf(value, 0) || math.IsNaN(value) {
				return nil, errors.New("attribute numbers must be finite")
			}
		case bool:
		default:
			return nil, errors.New("attribute values must be strings, numbers, or booleans")
		}
	}
	return attributes, nil
}

func vectorStoreFileChunking(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{"type":"other"}`), nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("chunking_strategy must be an object")
	}
	var kind string
	if json.Unmarshal(fields["type"], &kind) != nil {
		return nil, errors.New("chunking_strategy.type must be auto")
	}
	if kind == "static" {
		return nil, errVectorStoreStaticChunking
	}
	if kind != "auto" || !onlyJSONFields(raw, "type") {
		return nil, errors.New("chunking_strategy.type must be auto")
	}
	return []byte(`{"type":"other"}`), nil
}

func (handler *Handler) getVectorStoreFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	item, err := handler.readVectorStoreFile(request.Context(), principal.KeyID, request.PathValue("vector_store_id"), request.PathValue("file_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file is unavailable")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) readVectorStoreFile(ctx context.Context, keyID, storeID, fileID string) (vectorStoreFile, error) {
	return readVectorStoreFileFrom(ctx, handler.database, keyID, storeID, fileID)
}

func readVectorStoreFileFrom(ctx context.Context, query responseQueryer, keyID, storeID, fileID string) (vectorStoreFile, error) {
	row := query.QueryRowContext(ctx, `SELECT openai_vector_store_files.file_id,openai_vector_store_files.attributes_json,openai_vector_store_files.chunking_strategy_json,openai_vector_store_files.created_at,openai_files.bytes
		FROM openai_vector_store_files
		JOIN openai_vector_stores ON openai_vector_stores.id=openai_vector_store_files.vector_store_id
		JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id
		WHERE openai_vector_store_files.vector_store_id=? AND openai_vector_store_files.file_id=? AND openai_vector_stores.key_id=? AND (openai_vector_stores.expires_at IS NULL OR openai_vector_stores.expires_at>?) AND openai_files.key_id=? AND openai_files.expires_at>?`, storeID, fileID, keyID, time.Now().UnixMilli(), keyID, time.Now().UnixMilli())
	return scanVectorStoreFile(row, storeID)
}

func scanVectorStoreFile(scanner vectorStoreScanner, storeID string) (vectorStoreFile, error) {
	var item vectorStoreFile
	var attributes, chunking []byte
	var createdAt int64
	if err := scanner.Scan(&item.ID, &attributes, &chunking, &createdAt, &item.UsageBytes); err != nil {
		return item, err
	}
	if json.Unmarshal(attributes, &item.Attributes) != nil || json.Unmarshal(chunking, &item.ChunkingStrategy) != nil {
		return item, errors.New("invalid vector store file metadata")
	}
	item.Object, item.VectorStoreID, item.Status, item.CreatedAt = "vector_store.file", storeID, "completed", createdAt/1000
	return item, nil
}

func (handler *Handler) updateVectorStoreFile(response http.ResponseWriter, request *http.Request) {
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
	if json.Unmarshal(body, &fields) != nil || len(fields) != 1 || !onlyJSONFields(body, "attributes") {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body must contain attributes")
		return
	}
	attributes, err := vectorStoreFileAttributes(fields["attributes"])
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	attributesJSON, _ := json.Marshal(attributes)
	storeID, fileID := request.PathValue("vector_store_id"), request.PathValue("file_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var current []byte
		err = tx.QueryRowContext(request.Context(), `SELECT openai_vector_store_files.attributes_json FROM openai_vector_store_files
			JOIN openai_vector_stores ON openai_vector_stores.id=openai_vector_store_files.vector_store_id
			JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id
			WHERE openai_vector_store_files.vector_store_id=? AND openai_vector_store_files.file_id=? AND openai_vector_stores.key_id=? AND (openai_vector_stores.expires_at IS NULL OR openai_vector_stores.expires_at>?) AND openai_files.key_id=? AND openai_files.expires_at>?`, storeID, fileID, principal.KeyID, time.Now().UnixMilli(), principal.KeyID, time.Now().UnixMilli()).Scan(&current)
		if growth := int64(len(attributesJSON) - len(current)); err == nil && growth > 0 {
			err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 0, growth)
		}
		if err == nil {
			_, err = tx.ExecContext(request.Context(), `UPDATE openai_vector_store_files SET attributes_json=? WHERE vector_store_id=? AND file_id=?`, attributesJSON, storeID, fileID)
		}
		if err == nil {
			err = touchVectorStore(request.Context(), tx, principal.KeyID, storeID, time.Now().UnixMilli())
		}
	}
	var item vectorStoreFile
	if err == nil {
		item, err = readVectorStoreFileFrom(request.Context(), tx, principal.KeyID, storeID, fileID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	}
	if err != nil {
		handler.writeVectorStoreFileMutationError(response, err, "updated")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) deleteVectorStoreFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	storeID, fileID, now := request.PathValue("vector_store_id"), request.PathValue("file_id"), time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var result sql.Result
		result, err = tx.ExecContext(request.Context(), `DELETE FROM openai_vector_store_files WHERE vector_store_id=? AND file_id=?
			AND EXISTS(SELECT 1 FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?))
			AND EXISTS(SELECT 1 FROM openai_files WHERE id=? AND key_id=? AND expires_at>?)`, storeID, fileID, storeID, principal.KeyID, now, fileID, principal.KeyID, now)
		if err == nil {
			if count, _ := result.RowsAffected(); count != 1 {
				err = sql.ErrNoRows
			}
		}
		if err == nil {
			err = touchVectorStore(request.Context(), tx, principal.KeyID, storeID, now)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	}
	if err != nil {
		handler.writeVectorStoreFileMutationError(response, err, "deleted")
		return
	}
	writeJSON(response, map[string]any{"id": fileID, "object": "vector_store.file.deleted", "deleted": true})
}

func (handler *Handler) listVectorStoreFiles(response http.ResponseWriter, request *http.Request) {
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
	for _, name := range []string{"after", "before", "filter"} {
		if len(query[name]) > 1 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", name+" must be specified once")
			return
		}
	}
	after, before, filter := query.Get("after"), query.Get("before"), query.Get("filter")
	if after != "" && before != "" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after and before cannot be combined")
		return
	}
	if filter != "" && filter != "completed" && filter != "in_progress" && filter != "failed" && filter != "cancelled" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "filter is invalid")
		return
	}
	storeID, now := request.PathValue("vector_store_id"), time.Now().UnixMilli()
	var exists int
	if err = handler.database.QueryRowContext(request.Context(), `SELECT 1 FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, storeID, principal.KeyID, now).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	} else if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store files are unavailable")
		return
	}
	if filter != "" && filter != "completed" {
		writeJSON(response, map[string]any{"object": "list", "data": []vectorStoreFile{}, "has_more": false, "first_id": nil, "last_id": nil})
		return
	}
	predicate := `openai_vector_store_files.vector_store_id=? AND openai_files.key_id=? AND openai_files.expires_at>?`
	arguments := []any{storeID, principal.KeyID, now}
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
		err = handler.database.QueryRowContext(request.Context(), `SELECT openai_vector_store_files.created_at FROM openai_vector_store_files JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id WHERE `+predicate+` AND openai_vector_store_files.file_id=?`, storeID, principal.KeyID, now, cursor).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "cursor is not a valid Vector Store file cursor")
			return
		}
		if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store files are unavailable")
			return
		}
		predicate += ` AND (openai_vector_store_files.created_at ` + comparison + ` ? OR (openai_vector_store_files.created_at=? AND openai_vector_store_files.file_id ` + comparison + ` ?))`
		arguments = append(arguments, createdAt, createdAt, cursor)
	}
	arguments = append(arguments, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), `SELECT openai_vector_store_files.file_id,openai_vector_store_files.attributes_json,openai_vector_store_files.chunking_strategy_json,openai_vector_store_files.created_at,openai_files.bytes
		FROM openai_vector_store_files JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id WHERE `+predicate+`
		ORDER BY openai_vector_store_files.created_at `+order+`,openai_vector_store_files.file_id `+order+` LIMIT ?`, arguments...)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store files are unavailable")
		return
	}
	defer rows.Close()
	items := make([]vectorStoreFile, 0, limit)
	more := false
	for rows.Next() {
		item, scanErr := scanVectorStoreFile(rows, storeID)
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
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store files are unavailable")
		return
	}
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": nil, "last_id": nil}
	if len(items) > 0 {
		result["first_id"], result["last_id"] = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(response, result)
}

func (handler *Handler) writeVectorStoreFileMutationError(response http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, errVectorStoreResourceNotFound), errors.Is(err, sql.ErrNoRows):
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store or File not found")
	case errors.Is(err, errVectorStoreFileAttached):
		handler.writeError(response, "openai", http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, errRetainedResourceLimit):
		handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference resource retention limit reached")
	default:
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file could not be "+action)
	}
}
