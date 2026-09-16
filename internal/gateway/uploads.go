package gateway

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	maxOpenAIUploadParts = 16
	openAIUploadLifetime = time.Hour
)

type openAIUpload struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	ExpiresAt int64       `json:"expires_at"`
	Filename  string      `json:"filename"`
	Purpose   string      `json:"purpose"`
	Status    string      `json:"status"`
	File      *openAIFile `json:"file,omitempty"`
}

type openAIUploadRow struct {
	id, ownerID, keyID, filename, purpose, mimeType, status string
	expectedBytes, fileExpirySeconds                        int64
	fileID                                                  sql.NullString
	createdAt, expiresAt                                    int64
}

type openAIUploadCreate struct {
	Bytes        int64                    `json:"bytes"`
	Filename     string                   `json:"filename"`
	MimeType     string                   `json:"mime_type"`
	Purpose      string                   `json:"purpose"`
	ExpiresAfter *openAIUploadFileExpires `json:"expires_after"`
}

type openAIUploadFileExpires struct {
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}

type openAIUploadPart struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	CreatedAt int64  `json:"created_at"`
	UploadID  string `json:"upload_id"`
}

type openAIUploadComplete struct {
	PartIDs []string `json:"part_ids"`
	MD5     string   `json:"md5"`
}

var errOpenAIUploadPartTooLarge = errors.New("upload Part exceeds 16 MiB")

func (handler *Handler) createOpenAIUpload(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody+1))
	if err != nil || len(body) > maxInferenceBody {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var input openAIUploadCreate
	if decodeStrictJSON(body, &input) != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Request body contains unsupported or invalid fields")
		return
	}
	if input.Purpose != "batch" || input.MimeType != "application/jsonl" || input.Bytes < 1 || input.Bytes > maxFileBytes || !validFileName(input.Filename) || !strings.EqualFold(path.Ext(input.Filename), ".jsonl") {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload requires purpose batch, application/jsonl, a safe .jsonl filename, and bytes between 1 and 16777216")
		return
	}
	fileExpiry := int64(defaultFileExpiry / time.Second)
	if input.ExpiresAfter != nil {
		if input.ExpiresAfter.Anchor != "created_at" || input.ExpiresAfter.Seconds < 3600 || input.ExpiresAfter.Seconds > 2592000 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "expires_after requires anchor created_at and seconds between 3600 and 2592000")
			return
		}
		fileExpiry = input.ExpiresAfter.Seconds
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be created")
		return
	}
	now := time.Now()
	row := openAIUploadRow{id: "upload_" + token, ownerID: principal.OwnerUserID, keyID: principal.KeyID, filename: input.Filename, purpose: input.Purpose, mimeType: input.MimeType, status: "pending", expectedBytes: input.Bytes, fileExpirySeconds: fileExpiry, createdAt: now.UnixMilli(), expiresAt: now.Add(openAIUploadLifetime).UnixMilli()}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		reserved := int64(len(row.filename)+len(row.mimeType)) + row.expectedBytes + 28
		err = checkRetainedResourceCapacity(request.Context(), tx, row.ownerID, row.keyID, 1, reserved)
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,status,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,'pending',?,?)`, row.id, row.ownerID, row.keyID, row.filename, row.purpose, row.mimeType, row.expectedBytes, row.fileExpirySeconds, row.createdAt, row.expiresAt)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference resource retention limit reached")
			return
		}
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be created")
		return
	}
	writeJSON(response, row.public(nil))
}

func (handler *Handler) addOpenAIUploadPart(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	if !handler.acquireMultipart(response, "openai") {
		return
	}
	defer handler.releaseMultipart()
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	if request.ContentLength > maxFileUploadBody {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 17 MiB")
		return
	}
	limitedBody := http.MaxBytesReader(response, request.Body, maxFileUploadBody)
	content, err := parseOpenAIUploadPart(limitedBody, request.Header.Get("Content-Type"))
	if err == nil {
		_, err = io.Copy(io.Discard, limitedBody)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, errOpenAIUploadPartTooLarge) {
			handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		} else {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		}
		return
	}
	partToken, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload Part could not be created")
		return
	}
	uploadID, partID := request.PathValue("upload_id"), "part_"+partToken
	ciphertext, nonce, err := credentials.Seal(handler.masterKey, content, openAIUploadPartAdditionalData(uploadID, partID, principal.KeyID, int64(len(content))))
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload Part could not be created")
		return
	}
	now := time.Now()
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
	}
	var status string
	var expectedBytes, expiresAt, partCount, partBytes int64
	if err == nil {
		err = tx.QueryRowContext(request.Context(), `SELECT status,expected_bytes,expires_at FROM openai_uploads WHERE id=? AND key_id=?`, uploadID, principal.KeyID).Scan(&status, &expectedBytes, &expiresAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err == nil {
		err = tx.QueryRowContext(request.Context(), `SELECT COUNT(*),COALESCE(SUM(bytes),0) FROM openai_upload_parts WHERE upload_id=?`, uploadID).Scan(&partCount, &partBytes)
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload Part could not be created")
		return
	}
	if status != "pending" || expiresAt <= now.UnixMilli() {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload no longer accepts Parts")
		return
	}
	if partCount >= maxOpenAIUploadParts || int64(len(content)) > expectedBytes-partBytes {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload Part exceeds the declared Upload bounds")
		return
	}
	now = time.Now()
	if expiresAt <= now.UnixMilli() {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload no longer accepts Parts")
		return
	}
	result, err := tx.ExecContext(request.Context(), `INSERT INTO openai_upload_parts(id,upload_id,bytes,ciphertext,nonce,created_at) SELECT ?,id,?,?,?,? FROM openai_uploads WHERE id=? AND key_id=? AND status='pending' AND expires_at>?`, partID, len(content), ciphertext, nonce, now.UnixMilli(), uploadID, principal.KeyID, now.UnixMilli())
	if err == nil {
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
			err = errors.New("upload changed before Part creation")
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload Part could not be created")
		return
	}
	writeJSON(response, openAIUploadPart{ID: partID, Object: "upload.part", CreatedAt: now.Unix(), UploadID: uploadID})
}

func parseOpenAIUploadPart(body io.Reader, contentType string) ([]byte, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return nil, errors.New("content type must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(body, parameters["boundary"])
	var content []byte
	parts := 0
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		parts++
		if parts > 1 || part.FormName() != "data" {
			return nil, errors.New("multipart request must contain exactly one data field")
		}
		content, err = io.ReadAll(io.LimitReader(part, maxFileBytes+1))
		if err != nil {
			return nil, err
		}
		if len(content) > maxFileBytes {
			return nil, errOpenAIUploadPartTooLarge
		}
	}
	if parts != 1 || len(content) == 0 {
		return nil, errors.New("data must contain a non-empty Upload Part")
	}
	return content, nil
}

func (handler *Handler) completeOpenAIUpload(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxInferenceBody+1))
	if err != nil || len(body) > maxInferenceBody {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 16 MiB")
		return
	}
	var input openAIUploadComplete
	if decodeStrictJSON(body, &input) != nil || len(input.PartIDs) < 1 || len(input.PartIDs) > maxOpenAIUploadParts || input.MD5 != "" {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "part_ids must contain 1 to 16 unique IDs; non-empty md5 is unsupported")
		return
	}
	seen := make(map[string]struct{}, len(input.PartIDs))
	for _, id := range input.PartIDs {
		if id == "" {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "part_ids must contain 1 to 16 unique IDs; non-empty md5 is unsupported")
			return
		}
		if _, exists := seen[id]; exists {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "part_ids must contain 1 to 16 unique IDs; non-empty md5 is unsupported")
			return
		}
		seen[id] = struct{}{}
	}
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	uploadID := request.PathValue("upload_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
	}
	row, err := readOpenAIUploadRow(request.Context(), tx, uploadID, principal.KeyID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be completed")
		return
	}
	if row.status != "pending" || row.expiresAt <= time.Now().UnixMilli() {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload cannot be completed")
		return
	}
	content := make([]byte, 0, row.expectedBytes)
	for _, partID := range input.PartIDs {
		var size int64
		var ciphertext, nonce []byte
		err = tx.QueryRowContext(request.Context(), `SELECT bytes,ciphertext,nonce FROM openai_upload_parts WHERE id=? AND upload_id=?`, partID, row.id).Scan(&size, &ciphertext, &nonce)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "part_ids contains a Part that does not belong to the Upload")
			return
		}
		if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be completed")
			return
		}
		plain, openErr := credentials.Open(handler.masterKey, ciphertext, nonce, openAIUploadPartAdditionalData(row.id, partID, row.keyID, size))
		if openErr != nil || int64(len(plain)) != size {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be completed")
			return
		}
		if int64(len(plain)) > row.expectedBytes-int64(len(content)) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Uploaded Part bytes do not match the declared Upload size")
			return
		}
		content = append(content, plain...)
	}
	if int64(len(content)) != row.expectedBytes {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Uploaded Part bytes do not match the declared Upload size")
		return
	}
	now := time.Now()
	if row.expiresAt <= now.UnixMilli() {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload cannot be completed")
		return
	}
	file, err := handler.insertOpenAIFile(request.Context(), tx, row.ownerID, row.keyID, row.filename, "batch", content, now, now.Add(time.Duration(row.fileExpirySeconds)*time.Second))
	if err == nil {
		now = time.Now()
		if row.expiresAt <= now.UnixMilli() {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload cannot be completed")
			return
		}
		var result sql.Result
		result, err = tx.ExecContext(request.Context(), `UPDATE openai_uploads SET status='completed',file_id=?,completed_at=? WHERE id=? AND key_id=? AND status='pending' AND expires_at>?`, file.ID, now.UnixMilli(), row.id, row.keyID, now.UnixMilli())
		if err == nil {
			if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
				err = errors.New("upload changed before completion")
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `DELETE FROM openai_upload_parts WHERE upload_id=?`, row.id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be completed")
		return
	}
	row.status, row.fileID = "completed", sql.NullString{String: file.ID, Valid: true}
	writeJSON(response, row.public(&file))
}

func (handler *Handler) cancelOpenAIUpload(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	uploadID := request.PathValue("upload_id")
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
	}
	row, err := readOpenAIUploadRow(request.Context(), tx, uploadID, principal.KeyID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be cancelled")
		return
	}
	now := time.Now()
	if row.status != "pending" || row.expiresAt <= now.UnixMilli() {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "Upload cannot be cancelled")
		return
	}
	result, err := tx.ExecContext(request.Context(), `UPDATE openai_uploads SET status='cancelled',cancelled_at=? WHERE id=? AND key_id=? AND status='pending' AND expires_at>?`, now.UnixMilli(), row.id, row.keyID, now.UnixMilli())
	if err == nil {
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
			err = errors.New("upload changed before cancellation")
		}
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `DELETE FROM openai_upload_parts WHERE upload_id=?`, row.id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Upload could not be cancelled")
		return
	}
	row.status = "cancelled"
	writeJSON(response, row.public(nil))
}

func readOpenAIUploadRow(ctx context.Context, query responseQueryer, id, keyID string) (openAIUploadRow, error) {
	var row openAIUploadRow
	err := query.QueryRowContext(ctx, `SELECT id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,status,file_id,created_at,expires_at FROM openai_uploads WHERE id=? AND key_id=?`, id, keyID).Scan(&row.id, &row.ownerID, &row.keyID, &row.filename, &row.purpose, &row.mimeType, &row.expectedBytes, &row.fileExpirySeconds, &row.status, &row.fileID, &row.createdAt, &row.expiresAt)
	return row, err
}

func (row openAIUploadRow) public(file *openAIFile) openAIUpload {
	status := row.status
	if status == "pending" && row.expiresAt <= time.Now().UnixMilli() {
		status = "expired"
	}
	return openAIUpload{ID: row.id, Object: "upload", Bytes: row.expectedBytes, CreatedAt: row.createdAt / 1000, ExpiresAt: row.expiresAt / 1000, Filename: row.filename, Purpose: row.purpose, Status: status, File: file}
}

func openAIUploadPartAdditionalData(uploadID, partID, keyID string, size int64) []byte {
	return []byte(uploadID + "\x00" + partID + "\x00" + keyID + "\x00" + strconv.FormatInt(size, 10))
}
