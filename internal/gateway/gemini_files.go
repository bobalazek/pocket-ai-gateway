package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	maxGeminiFileBytes   = 8 << 20
	geminiFileLifetime   = 48 * time.Hour
	geminiUploadLifetime = time.Hour
	geminiFileScope      = "files:manage"
)

type geminiFile struct {
	ID          string
	DisplayName string
	MIMEType    string
	Bytes       int64
	CreatedAt   int64
	ExpiresAt   int64
}

func (item geminiFile) resource() map[string]any {
	return map[string]any{
		"name": "files/" + item.ID, "displayName": item.DisplayName,
		"mimeType": item.MIMEType, "sizeBytes": strconv.FormatInt(item.Bytes, 10),
		"createTime":     time.UnixMilli(item.CreatedAt).UTC().Format(time.RFC3339Nano),
		"expirationTime": time.UnixMilli(item.ExpiresAt).UTC().Format(time.RFC3339Nano),
		"uri":            geminiFileURI(item.ID), "state": "ACTIVE",
	}
}

func geminiFileURI(id string) string { return "pag-gemini://files/" + id }

func (handler *Handler) geminiFilePrincipal(w http.ResponseWriter, r *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(w, r, "gemini")
	if ok && !principalHasScope(principal.Scopes, geminiFileScope) {
		handler.writeError(w, "gemini", http.StatusForbidden, "PERMISSION_DENIED", "Files access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) geminiUploadPrincipal(w http.ResponseWriter, r *http.Request) (keys.Principal, bool) {
	_, queryKey := r.URL.Query()["key"]
	if len(r.Header.Values("x-goog-api-key")) > 0 || queryKey {
		return handler.geminiFilePrincipal(w, r)
	}
	var keyID, ownerID string
	err := handler.database.QueryRowContext(r.Context(), `SELECT key_id,owner_user_id FROM gemini_uploads WHERE id=? AND expires_at>?`, r.PathValue("upload_id"), time.Now().UnixMilli()).Scan(&keyID, &ownerID)
	if err != nil {
		handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "Upload not found")
		return keys.Principal{}, false
	}
	principal, err := handler.keys.Principal(r.Context(), keyID)
	if err != nil || principal.OwnerUserID != ownerID || !principalHasScope(principal.Scopes, geminiFileScope) {
		handler.writeError(w, "gemini", http.StatusForbidden, "PERMISSION_DENIED", "Upload key is unavailable")
		return keys.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) startGeminiFileUpload(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.geminiFilePrincipal(w, r)
	if !ok {
		return
	}
	if !strings.EqualFold(r.Header.Get("X-Goog-Upload-Protocol"), "resumable") || strings.TrimSpace(strings.ToLower(r.Header.Get("X-Goog-Upload-Command"))) != "start" {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Start a resumable file upload")
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("X-Goog-Upload-Header-Content-Length"), 10, 64)
	if err != nil || length < 1 || length > maxGeminiFileBytes {
		handler.writeError(w, "gemini", http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "File size must be between 1 byte and 8 MiB")
		return
	}
	mimeType, validMIME := geminiBaseMIME(r.Header.Get("X-Goog-Upload-Header-Content-Type"))
	if !validMIME {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "A valid file MIME type is required")
		return
	}
	var input struct {
		File struct {
			Name             string `json:"name"`
			DisplayName      string `json:"displayName"`
			DisplayNameSnake string `json:"display_name"`
			MIMEType         string `json:"mimeType"`
			MIMETypeSnake    string `json:"mime_type"`
			SizeBytes        string `json:"sizeBytes"`
		} `json:"file"`
	}
	metadata, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !uniqueJSONFields(metadata, 8) {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid or duplicate file metadata")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(metadata, &envelope) != nil || len(envelope) != 1 {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "File metadata is required")
		return
	}
	fileMetadata := bytes.TrimSpace(envelope["file"])
	if len(fileMetadata) < 2 || fileMetadata[0] != '{' {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "File metadata is required")
		return
	}
	var fileFields map[string]json.RawMessage
	if json.Unmarshal(fileMetadata, &fileFields) != nil {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid file metadata")
		return
	}
	for field := range fileFields {
		if field != "name" && field != "displayName" && field != "display_name" && field != "mimeType" && field != "mime_type" && field != "sizeBytes" {
			handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Unsupported file metadata field")
			return
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid file metadata")
		return
	}
	metadataMIME, metadataMIMEValid := geminiBaseMIME(input.File.MIMEType)
	metadataMIMESnake, metadataMIMESnakeValid := geminiBaseMIME(input.File.MIMETypeSnake)
	if err := decoder.Decode(new(any)); err != io.EOF || input.File.DisplayName != "" && input.File.DisplayNameSnake != "" || input.File.MIMEType != "" && input.File.MIMETypeSnake != "" || input.File.MIMEType != "" && (!metadataMIMEValid || metadataMIME != mimeType) || input.File.MIMETypeSnake != "" && (!metadataMIMESnakeValid || metadataMIMESnake != mimeType) || input.File.SizeBytes != "" && input.File.SizeBytes != strconv.FormatInt(length, 10) {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "File metadata does not match upload headers")
		return
	}
	requestedID := ""
	if input.File.Name != "" {
		var valid bool
		requestedID, valid = geminiRequestedID(input.File.Name)
		if !valid {
			handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid File resource name")
			return
		}
	}
	displayName := input.File.DisplayName
	if displayName == "" {
		displayName = input.File.DisplayNameSnake
	}
	if displayName == "" {
		displayName = r.Header.Get("X-Goog-Upload-File-Name")
	}
	if displayName == "" {
		displayName = "upload"
	}
	if !validGeminiDisplayName(displayName) {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid display name")
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload could not be started")
		return
	}
	now := time.Now().UnixMilli()
	tx, err := handler.database.BeginTx(r.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		_, err = tx.ExecContext(r.Context(), `DELETE FROM gemini_uploads WHERE id IN (SELECT id FROM gemini_uploads WHERE expires_at<=? LIMIT 32)`, now)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM gemini_files WHERE id IN (SELECT id FROM gemini_files WHERE expires_at<=? LIMIT 32)`, now)
	}
	if err == nil {
		err = checkRetainedResourceCapacity(r.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(displayName)+len(mimeType))+length+28)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO gemini_uploads(id,owner_user_id,key_id,display_name,mime_type,expected_bytes,requested_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, "gupl_"+token, principal.OwnerUserID, principal.KeyID, displayName, mimeType, length, requestedID, now, now+geminiUploadLifetime.Milliseconds())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(w, "gemini", http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Stored file retention limit reached")
			return
		}
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload could not be started")
		return
	}
	w.Header().Set("X-Goog-Upload-URL", handler.batchOrigin(r)+"/api/gemini/upload/v1beta/files/gupl_"+token)
	w.Header().Set("X-Goog-Upload-Status", "active")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{})
}

func validGeminiDisplayName(value string) bool {
	return len(value) >= 1 && len(value) <= 512 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func geminiBaseMIME(value string) (string, bool) {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	return mediaType, err == nil && strings.Contains(mediaType, "/") && !strings.Contains(mediaType, "*")
}

func geminiRequestedID(name string) (string, bool) {
	id, ok := strings.CutPrefix(name, "files/")
	if !ok || len(id) < 1 || len(id) > 40 || id[0] == '-' || id[len(id)-1] == '-' {
		return "", false
	}
	for _, char := range id {
		if char < 'a' || char > 'z' {
			if char < '0' || char > '9' {
				if char != '-' {
					return "", false
				}
			}
		}
	}
	return id, true
}

func validGeminiFileID(id string) bool {
	if len(id) < 1 || len(id) > 40 {
		return false
	}
	for _, char := range id {
		if char < 'a' || char > 'z' {
			if char < '0' || char > '9' {
				if char != '-' {
					return false
				}
			}
		}
	}
	return id[0] != '-' && id[len(id)-1] != '-'
}

func randomGeminiFileID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "gfile-" + hex.EncodeToString(raw[:]), nil
}

func geminiFileAAD(id, keyID, mimeType string, size int64) []byte {
	return []byte(id + "\x00" + keyID + "\x00" + mimeType + "\x00" + strconv.FormatInt(size, 10))
}

func (handler *Handler) uploadGeminiFileChunk(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.geminiUploadPrincipal(w, r)
	if !ok {
		return
	}
	command := strings.ToLower(strings.ReplaceAll(r.Header.Get("X-Goog-Upload-Command"), " ", ""))
	if command != "upload" && command != "upload,finalize" && command != "finalize" {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Unsupported upload command")
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("X-Goog-Upload-Offset"), 10, 64)
	if err != nil || offset < 0 || offset > maxGeminiFileBytes {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid upload offset")
		return
	}
	if r.ContentLength > maxGeminiFileBytes {
		handler.writeError(w, "gemini", http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "File exceeds 8 MiB")
		return
	}
	chunk, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGeminiFileBytes+1))
	if err != nil || len(chunk) > maxGeminiFileBytes {
		handler.writeError(w, "gemini", http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "File exceeds 8 MiB")
		return
	}
	if (command == "finalize" && len(chunk) != 0) || (command != "finalize" && len(chunk) == 0) {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid upload chunk")
		return
	}
	select {
	case handler.uploads <- struct{}{}:
		defer func() { <-handler.uploads }()
	default:
		handler.writeError(w, "gemini", http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Another upload is in progress")
		return
	}
	tx, err := handler.database.BeginTx(r.Context(), nil)
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload is unavailable")
		return
	}
	defer tx.Rollback()
	var upload struct {
		Name, MIMEType, RequestedID, Status, FileID string
		Expected, Received                          int64
		Ciphertext, Nonce                           []byte
	}
	err = tx.QueryRowContext(r.Context(), `SELECT display_name,mime_type,COALESCE(requested_id,''),status,COALESCE(file_id,''),expected_bytes,received_bytes,ciphertext,nonce FROM gemini_uploads WHERE id=? AND key_id=? AND expires_at>?`, r.PathValue("upload_id"), principal.KeyID, time.Now().UnixMilli()).Scan(&upload.Name, &upload.MIMEType, &upload.RequestedID, &upload.Status, &upload.FileID, &upload.Expected, &upload.Received, &upload.Ciphertext, &upload.Nonce)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "Upload not found")
		return
	}
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload is unavailable")
		return
	}
	if offset > upload.Received || int64(len(chunk)) > upload.Expected-offset {
		handler.writeError(w, "gemini", http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "Upload exceeds declared size")
		return
	}
	content := []byte{}
	if upload.Status == "completed" {
		_ = tx.Rollback()
		item, finalized, readErr := handler.readGeminiFile(r.Context(), principal.KeyID, upload.FileID)
		if readErr != nil {
			handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "File not found")
			return
		}
		if !bytes.Equal(finalized[offset:offset+int64(len(chunk))], chunk) {
			handler.writeError(w, "gemini", http.StatusConflict, "ABORTED", "Upload retry does not match committed bytes")
			return
		}
		if command == "upload" && offset+int64(len(chunk)) < upload.Expected {
			w.Header().Set("X-Goog-Upload-Status", "active")
			writeJSON(w, map[string]any{})
			return
		}
		w.Header().Set("X-Goog-Upload-Status", "final")
		writeJSON(w, map[string]any{"file": item.resource()})
		return
	}
	if upload.Received > 0 {
		content, err = credentials.Open(handler.masterKey, upload.Ciphertext, upload.Nonce, geminiFileAAD(r.PathValue("upload_id"), principal.KeyID, upload.MIMEType, upload.Received))
		if err != nil {
			handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload is unavailable")
			return
		}
	}
	if offset < upload.Received {
		if offset+int64(len(chunk)) > upload.Received || !bytes.Equal(content[offset:offset+int64(len(chunk))], chunk) {
			handler.writeError(w, "gemini", http.StatusConflict, "ABORTED", "Upload retry does not match committed bytes")
			return
		}
		if command == "upload" {
			w.Header().Set("X-Goog-Upload-Status", "active")
			writeJSON(w, map[string]any{})
			return
		}
	} else {
		content = append(content, chunk...)
	}
	if command != "upload" && int64(len(content)) != upload.Expected {
		handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Upload is incomplete")
		return
	}
	if command == "upload" {
		ciphertext, nonce, sealErr := credentials.Seal(handler.masterKey, content, geminiFileAAD(r.PathValue("upload_id"), principal.KeyID, upload.MIMEType, int64(len(content))))
		if sealErr == nil {
			_, sealErr = tx.ExecContext(r.Context(), `UPDATE gemini_uploads SET received_bytes=?,ciphertext=?,nonce=? WHERE id=? AND key_id=?`, len(content), ciphertext, nonce, r.PathValue("upload_id"), principal.KeyID)
		}
		if sealErr == nil {
			sealErr = tx.Commit()
		}
		if sealErr != nil {
			handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Upload could not be saved")
			return
		}
		w.Header().Set("X-Goog-Upload-Status", "active")
		writeJSON(w, map[string]any{})
		return
	}
	id := upload.RequestedID
	if id == "" {
		id, err = randomGeminiFileID()
	}
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "File could not be created")
		return
	}
	ciphertext, nonce, err := credentials.Seal(handler.masterKey, content, geminiFileAAD(id, principal.KeyID, upload.MIMEType, int64(len(content))))
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "File could not be created")
		return
	}
	now := time.Now().UnixMilli()
	item := geminiFile{ID: id, DisplayName: upload.Name, MIMEType: upload.MIMEType, Bytes: int64(len(content)), CreatedAt: now, ExpiresAt: now + geminiFileLifetime.Milliseconds()}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO gemini_files(id,owner_user_id,key_id,display_name,mime_type,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, principal.OwnerUserID, principal.KeyID, item.DisplayName, item.MIMEType, item.Bytes, ciphertext, nonce, item.CreatedAt, item.ExpiresAt)
	if err != nil {
		var sameKey int
		if tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM gemini_files WHERE id=? AND key_id=?`, item.ID, principal.KeyID).Scan(&sameKey) == nil && sameKey > 0 {
			handler.writeError(w, "gemini", http.StatusConflict, "ALREADY_EXISTS", "File name already exists for this key")
			return
		}
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE gemini_uploads SET status='completed',file_id=?,completed_at=?,received_bytes=expected_bytes,ciphertext=NULL,nonce=NULL WHERE id=? AND key_id=? AND status='pending'`, item.ID, now, r.PathValue("upload_id"), principal.KeyID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "File could not be created")
		return
	}
	w.Header().Set("X-Goog-Upload-Status", "final")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"file": item.resource()})
}

func (handler *Handler) readGeminiFile(ctx context.Context, keyID, id string) (geminiFile, []byte, error) {
	var item geminiFile
	var ciphertext, nonce []byte
	err := handler.database.QueryRowContext(ctx, `SELECT id,display_name,mime_type,bytes,ciphertext,nonce,created_at,expires_at FROM gemini_files WHERE id=? AND key_id=? AND expires_at>?`, id, keyID, time.Now().UnixMilli()).Scan(&item.ID, &item.DisplayName, &item.MIMEType, &item.Bytes, &ciphertext, &nonce, &item.CreatedAt, &item.ExpiresAt)
	if err != nil {
		return geminiFile{}, nil, err
	}
	content, err := credentials.Open(handler.masterKey, ciphertext, nonce, geminiFileAAD(id, keyID, item.MIMEType, item.Bytes))
	if err != nil {
		return geminiFile{}, nil, err
	}
	return item, content, nil
}

func (handler *Handler) getGeminiFile(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.geminiFilePrincipal(w, r)
	if !ok {
		return
	}
	var item geminiFile
	err := handler.database.QueryRowContext(r.Context(), `SELECT id,display_name,mime_type,bytes,created_at,expires_at FROM gemini_files WHERE id=? AND key_id=? AND expires_at>?`, r.PathValue("file_id"), principal.KeyID, time.Now().UnixMilli()).Scan(&item.ID, &item.DisplayName, &item.MIMEType, &item.Bytes, &item.CreatedAt, &item.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "File is unavailable")
		return
	}
	writeJSON(w, item.resource())
}

func (handler *Handler) deleteGeminiFile(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.geminiFilePrincipal(w, r)
	if !ok {
		return
	}
	result, err := handler.database.ExecContext(r.Context(), `DELETE FROM gemini_files WHERE id=? AND key_id=? AND expires_at>?`, r.PathValue("file_id"), principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "File could not be deleted")
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	writeJSON(w, map[string]any{})
}

func (handler *Handler) listGeminiFiles(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.geminiFilePrincipal(w, r)
	if !ok {
		return
	}
	pageSize := 10
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		pageSize, _ = strconv.Atoi(raw)
		if pageSize < 1 || pageSize > 100 {
			handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "pageSize must be between 1 and 100")
			return
		}
	}
	var cursorTime int64
	var cursorID string
	if token := r.URL.Query().Get("pageToken"); token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(decoded) > 128 {
			handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid pageToken")
			return
		}
		when, id, ok := strings.Cut(string(decoded), ":")
		cursorTime, err = strconv.ParseInt(when, 10, 64)
		if !ok || err != nil || !validGeminiFileID(id) {
			handler.writeError(w, "gemini", http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid pageToken")
			return
		}
		cursorID = id
	}
	rows, err := handler.database.QueryContext(r.Context(), `SELECT id,display_name,mime_type,bytes,created_at,expires_at FROM gemini_files WHERE key_id=? AND expires_at>? AND (?=0 OR created_at<? OR (created_at=? AND id<?)) ORDER BY created_at DESC,id DESC LIMIT ?`, principal.KeyID, time.Now().UnixMilli(), cursorTime, cursorTime, cursorTime, cursorID, pageSize+1)
	if err != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Files are unavailable")
		return
	}
	defer rows.Close()
	items := make([]geminiFile, 0, pageSize+1)
	for rows.Next() {
		var item geminiFile
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.MIMEType, &item.Bytes, &item.CreatedAt, &item.ExpiresAt); err != nil {
			handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Files are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		handler.writeError(w, "gemini", http.StatusServiceUnavailable, "UNAVAILABLE", "Files are unavailable")
		return
	}
	result := map[string]any{"files": make([]any, 0, min(pageSize, len(items)))}
	if len(items) > pageSize {
		items = items[:pageSize]
		last := items[len(items)-1]
		result["nextPageToken"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.CreatedAt, 10) + ":" + last.ID))
	}
	for _, item := range items {
		result["files"] = append(result["files"].([]any), item.resource())
	}
	writeJSON(w, result)
}
