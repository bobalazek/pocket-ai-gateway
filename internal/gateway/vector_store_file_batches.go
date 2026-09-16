package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

type vectorStoreFileBatch struct {
	ID            string                `json:"id"`
	Object        string                `json:"object"`
	CreatedAt     int64                 `json:"created_at"`
	VectorStoreID string                `json:"vector_store_id"`
	Status        string                `json:"status"`
	FileCounts    vectorStoreFileCounts `json:"file_counts"`
}

type vectorStoreFileBatchInput struct {
	fileID     string
	attributes []byte
	chunking   []byte
}

func (handler *Handler) createVectorStoreFileBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	body, err := readJSONBody(response, request)
	if err != nil {
		writeVectorStoreBodyError(handler, response, err)
		return
	}
	files, err := parseVectorStoreFileBatch(body)
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errVectorStoreStaticChunking) {
			code = "unsupported_feature"
		}
		handler.writeError(response, "openai", http.StatusBadRequest, code, err.Error())
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file batch could not be created")
		return
	}
	id, storeID, now := "vsfb_"+token, request.PathValue("vector_store_id"), time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		var exists int
		err = tx.QueryRowContext(request.Context(), `SELECT 1 FROM openai_vector_stores WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, storeID, principal.KeyID, now).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			err = errVectorStoreResourceNotFound
		}
	}
	if err == nil {
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(id)))
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_vector_store_file_batches(id,vector_store_id,created_at,file_count) VALUES(?,?,?,?)`, id, storeID, now, len(files))
	}
	for _, file := range files {
		if err != nil {
			break
		}
		err = attachVectorStoreFile(request.Context(), tx, principal.OwnerUserID, principal.KeyID, storeID, file.fileID, id, file.attributes, file.chunking, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeVectorStoreFileMutationError(response, err, "batched")
		return
	}
	item, err := handler.readVectorStoreFileBatch(request.Context(), principal.KeyID, storeID, id)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file batch is unavailable")
		return
	}
	writeJSON(response, item)
}

func parseVectorStoreFileBatch(body []byte) ([]vectorStoreFileBatchInput, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil || !onlyJSONFields(body, "file_ids", "files", "attributes", "chunking_strategy") {
		return nil, errors.New("request body must contain file_ids or files")
	}
	_, hasIDs := fields["file_ids"]
	_, hasFiles := fields["files"]
	if hasIDs == hasFiles {
		return nil, errors.New("exactly one of file_ids or files is required")
	}
	if hasIDs {
		return parseVectorStoreFileBatchIDs(fields)
	}
	if len(fields["attributes"]) > 0 || len(fields["chunking_strategy"]) > 0 {
		return nil, errors.New("global attributes and chunking_strategy cannot be combined with files")
	}
	var entries []json.RawMessage
	if json.Unmarshal(fields["files"], &entries) != nil || len(entries) == 0 || len(entries) > 2000 {
		return nil, errors.New("files must contain between 1 and 2000 entries")
	}
	result := make([]vectorStoreFileBatchInput, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil || !onlyJSONFields(raw, "file_id", "attributes", "chunking_strategy") {
			return nil, errors.New("each files entry must contain file_id and optional attributes or chunking_strategy")
		}
		file, err := parseVectorStoreFileBatchItem(item)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[file.fileID]; duplicate {
			return nil, errors.New("file IDs must be unique")
		}
		seen[file.fileID] = struct{}{}
		result = append(result, file)
	}
	return result, nil
}

func parseVectorStoreFileBatchIDs(fields map[string]json.RawMessage) ([]vectorStoreFileBatchInput, error) {
	var ids []string
	if json.Unmarshal(fields["file_ids"], &ids) != nil || len(ids) == 0 || len(ids) > 2000 {
		return nil, errors.New("file_ids must contain between 1 and 2000 IDs")
	}
	attributes, err := vectorStoreFileAttributes(fields["attributes"])
	if err != nil {
		return nil, err
	}
	attributesJSON, _ := json.Marshal(attributes)
	chunking, err := vectorStoreFileChunking(fields["chunking_strategy"])
	if err != nil {
		return nil, err
	}
	result := make([]vectorStoreFileBatchInput, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("file IDs must be non-empty strings")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("file IDs must be unique")
		}
		seen[id] = struct{}{}
		result = append(result, vectorStoreFileBatchInput{fileID: id, attributes: attributesJSON, chunking: chunking})
	}
	return result, nil
}

func parseVectorStoreFileBatchItem(fields map[string]json.RawMessage) (vectorStoreFileBatchInput, error) {
	var result vectorStoreFileBatchInput
	if json.Unmarshal(fields["file_id"], &result.fileID) != nil || result.fileID == "" {
		return result, errors.New("file_id must be a non-empty string")
	}
	attributes, err := vectorStoreFileAttributes(fields["attributes"])
	if err != nil {
		return result, err
	}
	result.attributes, _ = json.Marshal(attributes)
	result.chunking, err = vectorStoreFileChunking(fields["chunking_strategy"])
	return result, err
}

func (handler *Handler) getVectorStoreFileBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	item, err := handler.readVectorStoreFileBatch(request.Context(), principal.KeyID, request.PathValue("vector_store_id"), request.PathValue("batch_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file batch not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file batch is unavailable")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) cancelVectorStoreFileBatch(response http.ResponseWriter, request *http.Request) {
	// Local attachments commit atomically, so there is no observable in-progress
	// state to cancel. Returning the terminal batch keeps cancellation idempotent.
	handler.getVectorStoreFileBatch(response, request)
}

func (handler *Handler) listVectorStoreFileBatchFiles(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	batchID, storeID := request.PathValue("batch_id"), request.PathValue("vector_store_id")
	if _, err := handler.readVectorStoreFileBatch(request.Context(), principal.KeyID, storeID, batchID); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file batch not found")
		return
	} else if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file batch is unavailable")
		return
	}
	handler.listVectorStoreFilesPage(response, request, batchID)
}

func (handler *Handler) readVectorStoreFileBatch(ctx context.Context, keyID, storeID, batchID string) (vectorStoreFileBatch, error) {
	var item vectorStoreFileBatch
	var createdAt, fileCount int64
	err := handler.database.QueryRowContext(ctx, `SELECT openai_vector_store_file_batches.id,openai_vector_store_file_batches.created_at,openai_vector_store_file_batches.file_count
		FROM openai_vector_store_file_batches
		JOIN openai_vector_stores ON openai_vector_stores.id=openai_vector_store_file_batches.vector_store_id
		WHERE openai_vector_store_file_batches.id=? AND openai_vector_store_file_batches.vector_store_id=? AND openai_vector_stores.key_id=?
		AND (openai_vector_stores.expires_at IS NULL OR openai_vector_stores.expires_at>?)`, batchID, storeID, keyID, time.Now().UnixMilli()).Scan(&item.ID, &createdAt, &fileCount)
	if err != nil {
		return item, err
	}
	item.Object = "vector_store.files_batch"
	item.CreatedAt = createdAt / 1000
	item.VectorStoreID = storeID
	item.Status = "completed"
	item.FileCounts = vectorStoreFileCounts{Completed: fileCount, Total: fileCount}
	return item, nil
}
