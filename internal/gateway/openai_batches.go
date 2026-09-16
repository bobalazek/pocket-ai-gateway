package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

type openAIBatchRow struct {
	id, inputFileID, endpoint, completionWindow, modelID, status string
	metadata                                                     []byte
	outputExpirySeconds                                          int64
	outputFileID, errorFileID                                    sql.NullString
	requestTotal, requestCompleted, requestFailed                int64
	usageKnown                                                   bool
	inputTokens, outputTokens, cachedTokens, reasoningTokens     int64
	createdAt, inProgressAt, expiresAt                           int64
	cancellingAt, terminalAt                                     sql.NullInt64
}

type openAIBatchCreate struct {
	InputFileID      string                    `json:"input_file_id"`
	Endpoint         string                    `json:"endpoint"`
	CompletionWindow string                    `json:"completion_window"`
	Metadata         json.RawMessage           `json:"metadata"`
	OutputExpires    *openAIBatchOutputExpires `json:"output_expires_after"`
}

type openAIBatchOutputExpires struct {
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}

type openAIBatchInputLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

func (handler *Handler) openAIBatchPrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "openai")
	if ok && !principalHasScope(principal.Scopes, "batches:manage") {
		handler.writeError(response, "openai", http.StatusForbidden, "permission_denied", "Batches access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) createOpenAIBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.openAIBatchPrincipal(response, request)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody+1))
	if err != nil || len(body) > maxInferenceBody {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var input openAIBatchCreate
	if err = decodeStrictJSON(body, &input); err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body contains unsupported or invalid fields")
		return
	}
	_, scope, _, supportedEndpoint := openAIBatchEndpoint(input.Endpoint)
	if input.InputFileID == "" || !supportedEndpoint || input.CompletionWindow != "24h" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "input_file_id, a supported endpoint, and completion_window 24h are required")
		return
	}
	if !principalHasScope(principal.Scopes, scope) {
		handler.writeError(response, "openai", http.StatusForbidden, "permission_denied", "Batch creation requires access to its endpoint")
		return
	}
	metadata, err := validateChatMetadata(input.Metadata, false)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	outputExpiry := int64(openAIBatchRetention / time.Second)
	if input.OutputExpires != nil {
		if input.OutputExpires.Anchor != "created_at" || input.OutputExpires.Seconds < 3600 || input.OutputExpires.Seconds > 2592000 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "output_expires_after requires anchor created_at and seconds between 3600 and 2592000")
			return
		}
		outputExpiry = input.OutputExpires.Seconds
	}
	// ponytail: serialized 16 MiB decrypt/parse/seal bounds peak memory; use chunked AEAD before raising concurrency.
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	file, content, err := handler.loadOpenAIFileContent(request.Context(), principal.KeyID, input.InputFileID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && file.Purpose != "batch" {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Input File not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch input File is unavailable")
		return
	}
	items, modelID, err := parseOpenAIBatchInput(content, input.Endpoint)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be created")
		return
	}
	batchID := "batch_" + token
	reservedResultBytes := openAIBatchResultReservation(int64(len(items)))
	type sealedItem struct {
		customID, resultID string
		body               []byte
		ciphertext, nonce  []byte
	}
	sealed := make([]sealedItem, 0, len(items))
	incomingBytes := int64(len(metadata))
	queueBytes := int64(0)
	for index, item := range items {
		resultToken, tokenErr := credentials.RandomToken(18)
		if tokenErr != nil {
			err = tokenErr
			break
		}
		ciphertext, nonce, sealErr := handler.sealOpenAIBatchPayload(batchID, principal.KeyID, item.CustomID, int64(index+1), "request", item.Body)
		if sealErr != nil {
			err = sealErr
			break
		}
		sealed = append(sealed, sealedItem{customID: item.CustomID, resultID: "batch_req_" + resultToken, body: item.Body, ciphertext: ciphertext, nonce: nonce})
		queueBytes += int64(len(item.Body))
		incomingBytes += int64(len(ciphertext)+len(nonce)) + reservedResultBytes
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be created")
		return
	}
	now := time.Now()
	row := openAIBatchRow{id: batchID, inputFileID: file.ID, endpoint: input.Endpoint, completionWindow: input.CompletionWindow, modelID: modelID, status: "in_progress", metadata: metadata, outputExpirySeconds: outputExpiry, requestTotal: int64(len(items)), createdAt: now.UnixMilli(), inProgressAt: now.UnixMilli(), expiresAt: now.Add(openAIBatchProcessingWindow).UnixMilli()}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = checkBackgroundQueueCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, int64(len(items)), queueBytes)
	}
	if err == nil {
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, int64(len(items))+1, incomingBytes)
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,cancel_requested,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES(?,?,?,?,?,?,?,'in_progress',0,?,?,?,?,?,?,?)`, row.id, principal.OwnerUserID, principal.KeyID, row.inputFileID, row.endpoint, row.completionWindow, row.modelID, row.metadata, row.outputExpirySeconds, row.requestTotal, row.createdAt, row.inProgressAt, row.expiresAt, now.Add(openAIBatchRetention).UnixMilli())
	}
	for index, item := range sealed {
		if err != nil {
			break
		}
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,state,reserved_result_bytes) VALUES(?,?,?,?,?,?,?,'queued',?)`, row.id, index+1, item.customID, item.resultID, len(item.body), item.ciphertext, item.nonce, reservedResultBytes)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) || errors.Is(err, errBackgroundQueueLimit) {
			handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Batch capacity is exhausted")
			return
		}
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be created")
		return
	}
	select {
	case handler.wake <- struct{}{}:
	default:
	}
	handler.writeOpenAIBatch(response, row)
}

func openAIBatchEndpoint(endpoint string) (dialect, scope, upstreamPath string, ok bool) {
	switch endpoint {
	case "/v1/responses":
		return "responses", "responses:generate", "responses", true
	case "/v1/chat/completions":
		return "openai", "chat:generate", "chat/completions", true
	case "/v1/embeddings":
		return "openai", "embeddings:generate", "embeddings", true
	case "/v1/moderations":
		return "openai", "moderations:classify", "moderations", true
	case "/v1/images/generations":
		return "openai", "images:generate", "images/generations", true
	default:
		return "", "", "", false
	}
}

func parseOpenAIBatchInput(content []byte, endpoint string) ([]openAIBatchInputLine, string, error) {
	if _, _, _, ok := openAIBatchEndpoint(endpoint); !ok {
		return nil, "", errors.New("batch endpoint is unsupported")
	}
	lines := bytes.Split(content, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 1 || len(lines) > openAIBatchMaxItems {
		return nil, "", errors.New("input File must contain between 1 and 4 JSONL lines")
	}
	items := make([]openAIBatchInputLine, 0, len(lines))
	seen := map[string]struct{}{}
	modelID := ""
	for _, line := range lines {
		var item openAIBatchInputLine
		if len(bytes.TrimSpace(line)) == 0 || !utf8.Valid(line) || decodeStrictJSON(line, &item) != nil {
			return nil, "", errors.New("each input File line must be one JSON object with only custom_id, method, url, and body")
		}
		if !utf8.ValidString(item.CustomID) || len(item.CustomID) < 1 || len(item.CustomID) > 64 {
			return nil, "", errors.New("custom_id must contain between 1 and 64 UTF-8 bytes")
		}
		if _, exists := seen[item.CustomID]; exists {
			return nil, "", errors.New("custom_id values must be unique")
		}
		seen[item.CustomID] = struct{}{}
		if item.Method != http.MethodPost || item.URL != endpoint {
			return nil, "", errors.New("each input File line must use POST with the Batch endpoint")
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(item.Body, &envelope) != nil || envelope == nil {
			return nil, "", errors.New("each input File body must be a JSON object")
		}
		var itemModel string
		if json.Unmarshal(envelope["model"], &itemModel) != nil || strings.TrimSpace(itemModel) == "" || len(itemModel) > 200 {
			return nil, "", errors.New("each input File body requires a valid model")
		}
		if modelID != "" && modelID != itemModel {
			return nil, "", errors.New("all input File requests must use the same model")
		}
		modelID = itemModel
		switch endpoint {
		case "/v1/responses":
			if enabled, fieldErr := jsonBoolean(envelope, "stream", false); fieldErr != nil || enabled {
				return nil, "", errors.New("stream must be false or omitted in Batches")
			}
			if enabled, fieldErr := jsonBoolean(envelope, "background", false); fieldErr != nil || enabled {
				return nil, "", errors.New("background must be false or omitted in Batches")
			}
			for _, field := range []string{"conversation", "previous_response_id"} {
				raw := bytes.TrimSpace(envelope[field])
				if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) {
					return nil, "", errors.New(field + " is not supported in Batches")
				}
			}
			if store, storeErr := jsonBoolean(envelope, "store", false); storeErr != nil || store {
				return nil, "", errors.New("store must be false or omitted in Batches")
			}
		case "/v1/chat/completions":
			if enabled, fieldErr := jsonBoolean(envelope, "stream", false); fieldErr != nil || enabled {
				return nil, "", errors.New("stream must be false or omitted in Batches")
			}
			var messages []json.RawMessage
			if json.Unmarshal(envelope["messages"], &messages) != nil || len(messages) == 0 {
				return nil, "", errors.New("chat Batch requests require a non-empty messages array")
			}
			if _, exists := envelope["web_search_options"]; exists {
				return nil, "", errors.New("web_search_options is not supported in Batches")
			}
			if store, storeErr := jsonBoolean(envelope, "store", false); storeErr != nil || store {
				return nil, "", errors.New("store must be false or omitted in Batches")
			}
		case "/v1/embeddings":
			for _, field := range []string{"stream", "store", "background", "conversation", "previous_response_id", "tools", "web_search_options"} {
				if _, exists := envelope[field]; exists {
					return nil, "", errors.New(field + " is not supported in Embeddings Batches")
				}
			}
			if _, err := validateEmbedding(envelope); err != nil {
				return nil, "", err
			}
		case "/v1/moderations":
			if err := validateModeration(envelope); err != nil {
				return nil, "", err
			}
		case "/v1/images/generations":
			if err := validateImageGeneration(envelope); err != nil {
				return nil, "", err
			}
		}
		if containsLocalFileReference(item.Body) {
			return nil, "", errors.New("gateway File references are not supported in Batch requests")
		}
		if endpoint == "/v1/responses" {
			var responseInput any
			if raw, exists := envelope["input"]; exists && json.Unmarshal(raw, &responseInput) == nil {
				if err := rejectCompactReferences(responseInput); err != nil {
					return nil, "", err
				}
			}
		}
		if endpoint != "/v1/embeddings" {
			if raw, exists := envelope["tools"]; exists {
				var tools []map[string]json.RawMessage
				if json.Unmarshal(raw, &tools) != nil {
					return nil, "", errors.New("tools must be an array of function tools in Batches")
				}
				for _, tool := range tools {
					var kind string
					if json.Unmarshal(tool["type"], &kind) != nil || kind != "function" {
						return nil, "", errors.New("hosted tools are not supported in Batches")
					}
				}
			}
		}
		items = append(items, item)
	}
	return items, modelID, nil
}

func decodeStrictJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func (handler *Handler) getOpenAIBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.openAIBatchPrincipal(response, request)
	if !ok {
		return
	}
	row, err := handler.loadOpenAIBatch(request.Context(), principal.KeyID, request.PathValue("batch_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Batch not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch is unavailable")
		return
	}
	handler.writeOpenAIBatch(response, row)
}

func (handler *Handler) listOpenAIBatches(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.openAIBatchPrincipal(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for name, values := range query {
		if name != "after" && name != "limit" || len(values) != 1 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Only one after and limit value are supported")
			return
		}
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	predicate := `key_id=? AND retention_expires_at>?`
	arguments := []any{principal.KeyID, time.Now().UnixMilli()}
	after := query.Get("after")
	if after != "" {
		var createdAt int64
		if err := handler.database.QueryRowContext(request.Context(), `SELECT created_at FROM openai_batches WHERE id=? AND key_id=? AND retention_expires_at>?`, after, principal.KeyID, time.Now().UnixMilli()).Scan(&createdAt); errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after is not a valid Batch cursor")
			return
		} else if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batches are unavailable")
			return
		}
		predicate += ` AND (created_at<? OR (created_at=? AND id<?))`
		arguments = append(arguments, createdAt, createdAt, after)
	}
	arguments = append(arguments, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), openAIBatchSelect+` WHERE `+predicate+` ORDER BY created_at DESC,id DESC LIMIT ?`, arguments...)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batches are unavailable")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	more := false
	for rows.Next() {
		row, scanErr := scanOpenAIBatch(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		if len(items) == limit {
			more = true
			break
		}
		value, valueErr := openAIBatchValue(row)
		if valueErr != nil {
			err = valueErr
			break
		}
		items = append(items, value)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batches are unavailable")
		return
	}
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": nil, "last_id": nil}
	if len(items) > 0 {
		result["first_id"], result["last_id"] = items[0]["id"], items[len(items)-1]["id"]
	}
	writeJSON(response, result)
}

func (handler *Handler) cancelOpenAIBatch(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.openAIBatchPrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("batch_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be cancelled")
		return
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(request.Context(), `SELECT status FROM openai_batches WHERE id=? AND key_id=? AND retention_expires_at>?`, id, principal.KeyID, time.Now().UnixMilli()).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Batch not found")
		return
	} else if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be cancelled")
		return
	}
	if status == "in_progress" || status == "finalizing" || status == "cancelling" {
		_, err = tx.ExecContext(request.Context(), `UPDATE openai_batches SET status='cancelling',cancel_requested=1,cancelling_at=COALESCE(cancelling_at,?) WHERE id=? AND key_id=? AND status IN ('in_progress','finalizing','cancelling')`, time.Now().UnixMilli(), id, principal.KeyID)
		if err == nil {
			err = handler.cancelOpenAIBatchQueuedTx(request.Context(), tx, id, "batch_cancelled")
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be cancelled")
		return
	}
	if err = handler.finalizeOpenAIBatch(request.Context(), id); err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch could not be cancelled")
		return
	}
	handler.cancelOpenAIBatchActive(id)
	row, err := handler.loadOpenAIBatch(request.Context(), principal.KeyID, id)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch is unavailable")
		return
	}
	handler.writeOpenAIBatch(response, row)
}

const openAIBatchSelect = `SELECT id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,output_file_id,error_file_id,request_total,request_completed,request_failed,usage_known,usage_input_tokens,usage_output_tokens,usage_cached_tokens,usage_reasoning_tokens,created_at,in_progress_at,expires_at,cancelling_at,terminal_at FROM openai_batches`

func (handler *Handler) loadOpenAIBatch(ctx context.Context, keyID, id string) (openAIBatchRow, error) {
	return scanOpenAIBatch(handler.database.QueryRowContext(ctx, openAIBatchSelect+` WHERE id=? AND key_id=? AND retention_expires_at>?`, id, keyID, time.Now().UnixMilli()))
}

type openAIBatchScanner interface{ Scan(...any) error }

func scanOpenAIBatch(scanner openAIBatchScanner) (openAIBatchRow, error) {
	var row openAIBatchRow
	err := scanner.Scan(&row.id, &row.inputFileID, &row.endpoint, &row.completionWindow, &row.modelID, &row.status, &row.metadata, &row.outputExpirySeconds, &row.outputFileID, &row.errorFileID, &row.requestTotal, &row.requestCompleted, &row.requestFailed, &row.usageKnown, &row.inputTokens, &row.outputTokens, &row.cachedTokens, &row.reasoningTokens, &row.createdAt, &row.inProgressAt, &row.expiresAt, &row.cancellingAt, &row.terminalAt)
	return row, err
}

func (handler *Handler) writeOpenAIBatch(response http.ResponseWriter, row openAIBatchRow) {
	value, err := openAIBatchValue(row)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Batch is unavailable")
		return
	}
	writeJSON(response, value)
}

func openAIBatchValue(row openAIBatchRow) (map[string]any, error) {
	var metadata any
	if json.Unmarshal(row.metadata, &metadata) != nil {
		return nil, errors.New("invalid stored metadata")
	}
	value := map[string]any{
		"id": row.id, "object": "batch", "endpoint": row.endpoint, "errors": nil,
		"input_file_id": row.inputFileID, "completion_window": row.completionWindow,
		"status": row.status, "output_file_id": nil, "error_file_id": nil,
		"created_at": row.createdAt / 1000, "in_progress_at": row.inProgressAt / 1000,
		"expires_at": row.expiresAt / 1000, "finalizing_at": nil, "completed_at": nil,
		"failed_at": nil, "expired_at": nil, "cancelling_at": nil, "cancelled_at": nil,
		"request_counts": map[string]int64{"total": row.requestTotal, "completed": row.requestCompleted, "failed": row.requestFailed},
		"metadata":       metadata, "model": row.modelID, "usage": nil,
	}
	if row.outputFileID.Valid {
		value["output_file_id"] = row.outputFileID.String
	}
	if row.errorFileID.Valid {
		value["error_file_id"] = row.errorFileID.String
	}
	if row.cancellingAt.Valid {
		value["cancelling_at"] = row.cancellingAt.Int64 / 1000
	}
	if row.terminalAt.Valid {
		switch row.status {
		case "completed":
			value["completed_at"] = row.terminalAt.Int64 / 1000
		case "cancelled":
			value["cancelled_at"] = row.terminalAt.Int64 / 1000
		case "expired":
			value["expired_at"] = row.terminalAt.Int64 / 1000
		}
		if row.usageKnown && row.outputTokens > math.MaxInt64-row.inputTokens {
			return nil, errors.New("invalid stored usage")
		}
		if row.usageKnown {
			value["usage"] = map[string]any{
				"input_tokens": row.inputTokens, "output_tokens": row.outputTokens, "total_tokens": row.inputTokens + row.outputTokens,
				"input_tokens_details":  map[string]int64{"cached_tokens": row.cachedTokens},
				"output_tokens_details": map[string]int64{"reasoning_tokens": row.reasoningTokens},
			}
		}
	}
	return value, nil
}
