package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	maxFileBytes      = 16 << 20
	maxFileUploadBody = 17 << 20
	defaultFileExpiry = 30 * 24 * time.Hour
)

type openAIFile struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
}

type fileUpload struct {
	filename string
	content  []byte
	purpose  string
	expires  time.Duration
}

var errFileTooLarge = errors.New("file exceeds 16 MiB")

func (handler *Handler) filePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	principal, ok := handler.authenticate(response, request, "openai")
	if ok && !principalHasScope(principal.Scopes, "files:manage") {
		handler.writeError(response, "openai", http.StatusForbidden, "permission_denied", "Files access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) createFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	if !handler.acquireMultipart(response, "openai") {
		return
	}
	defer handler.releaseMultipart()
	if request.ContentLength > maxFileUploadBody {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 17 MiB")
		return
	}
	limitedBody := http.MaxBytesReader(response, request.Body, maxFileUploadBody)
	upload, err := parseFileUpload(limitedBody, request.Header.Get("Content-Type"))
	if err == nil {
		_, err = io.Copy(io.Discard, limitedBody)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 17 MiB")
		} else if errors.Is(err, errFileTooLarge) {
			handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		} else {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		}
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File could not be created")
		return
	}
	id := "file_" + token
	ciphertext, nonce, err := credentials.Seal(handler.masterKey, upload.content, fileAdditionalData(id, principal.KeyID, upload.purpose, int64(len(upload.content))))
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File could not be created")
		return
	}
	now := time.Now()
	item := openAIFile{ID: id, Object: "file", Bytes: int64(len(upload.content)), CreatedAt: now.Unix(), ExpiresAt: now.Add(upload.expires).Unix(), Filename: upload.filename, Purpose: upload.purpose, Status: "processed"}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(upload.filename)+len(ciphertext)+len(nonce)))
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,client_purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, item.ID, principal.OwnerUserID, principal.KeyID, item.Filename, storedFilePurpose(item.Purpose), item.Purpose, item.Bytes, ciphertext, nonce, now.UnixMilli(), now.Add(upload.expires).UnixMilli())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Stored inference resource retention limit reached")
			return
		}
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File could not be created")
		return
	}
	writeJSON(response, item)
}

func parseFileUpload(body io.Reader, contentType string) (fileUpload, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return fileUpload{}, errors.New("content type must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(body, parameters["boundary"])
	upload := fileUpload{expires: defaultFileExpiry}
	seen := map[string]bool{}
	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fileUpload{}, err
		}
		parts++
		if parts > 8 {
			return fileUpload{}, errors.New("multipart request has too many fields")
		}
		name := part.FormName()
		if len(name) > 64 {
			return fileUpload{}, errors.New("multipart field name is too long")
		}
		if seen[name] {
			return fileUpload{}, errors.New("multipart fields must be provided once")
		}
		seen[name] = true
		if name == "file" {
			filename := part.FileName()
			if filename == "" || !validFileName(filename) {
				return fileUpload{}, errors.New("file must have a safe filename")
			}
			content, err := io.ReadAll(io.LimitReader(part, maxFileBytes+1))
			if err != nil {
				return fileUpload{}, err
			}
			if len(content) > maxFileBytes {
				return fileUpload{}, errFileTooLarge
			}
			if len(content) == 0 {
				return fileUpload{}, errors.New("file must be non-empty")
			}
			upload.filename, upload.content = filename, content
			continue
		}
		if part.FileName() != "" {
			return fileUpload{}, errors.New("only the file field may contain an upload")
		}
		value, err := io.ReadAll(io.LimitReader(part, 1025))
		if err != nil {
			return fileUpload{}, err
		}
		if len(value) > 1024 {
			return fileUpload{}, errors.New("multipart field is too large")
		}
		text := strings.TrimSpace(string(value))
		switch name {
		case "purpose":
			if !validFilePurpose(text, false) {
				return fileUpload{}, errors.New("purpose must be assistants, batch, fine-tune, vision, user_data, or evals")
			}
			upload.purpose = text
		case "expires_after[anchor]":
			if text != "created_at" {
				return fileUpload{}, errors.New("expires_after.anchor must be created_at")
			}
		case "expires_after[seconds]":
			seconds, parseErr := strconv.ParseInt(text, 10, 64)
			if parseErr != nil || seconds < 3600 || seconds > 2592000 {
				return fileUpload{}, errors.New("expires_after.seconds must be between 3600 and 2592000")
			}
			upload.expires = time.Duration(seconds) * time.Second
		default:
			return fileUpload{}, errors.New("multipart fields must be file, purpose, expires_after[anchor], or expires_after[seconds]")
		}
	}
	if !seen["file"] || !seen["purpose"] {
		return fileUpload{}, errors.New("file and purpose are required")
	}
	if seen["expires_after[anchor]"] != seen["expires_after[seconds]"] {
		return fileUpload{}, errors.New("expires_after requires anchor and seconds")
	}
	if filePurposeRequiresJSONL(upload.purpose) && !strings.EqualFold(path.Ext(upload.filename), ".jsonl") {
		return fileUpload{}, errors.New("batch, fine-tune, and evals files must use a .jsonl filename")
	}
	return upload, nil
}

func validFilePurpose(value string, generated bool) bool {
	switch value {
	case "assistants", "batch", "fine-tune", "vision", "user_data", "evals":
		return true
	case "batch_output":
		return generated
	default:
		return false
	}
}

func filePurposeRequiresJSONL(value string) bool {
	return value == "batch" || value == "fine-tune" || value == "evals"
}

func storedFilePurpose(value string) string {
	if value == "batch_output" {
		return value
	}
	return "batch"
}

func validFileName(filename string) bool {
	return len(filename) > 0 && len(filename) <= 512 && utf8.ValidString(filename) && !strings.ContainsAny(filename, "\x00\r\n") && path.Base(strings.ReplaceAll(filename, `\`, "/")) == filename
}

func (handler *Handler) getFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	item, err := handler.readFile(request.Context(), principal.KeyID, request.PathValue("file_id"))
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "File not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File is unavailable")
		return
	}
	writeJSON(response, item)
}

func (handler *Handler) readFile(ctx context.Context, keyID, id string) (openAIFile, error) {
	var item openAIFile
	var createdAt, expiresAt int64
	err := handler.database.QueryRowContext(ctx, `SELECT id,filename,COALESCE(client_purpose,purpose),bytes,created_at,expires_at FROM openai_files WHERE id=? AND key_id=? AND expires_at>?`, id, keyID, time.Now().UnixMilli()).Scan(&item.ID, &item.Filename, &item.Purpose, &item.Bytes, &createdAt, &expiresAt)
	item.Object, item.Status, item.CreatedAt, item.ExpiresAt = "file", "processed", createdAt/1000, expiresAt/1000
	return item, err
}

func (handler *Handler) fileContent(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	// ponytail: one buffered transfer caps AEAD ciphertext+plaintext memory; use chunked encryption before raising concurrency.
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	id := request.PathValue("file_id")
	item, content, err := handler.loadOpenAIFileContent(request.Context(), principal.KeyID, id)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "File not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File content is unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Filename}))
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Content-Length", strconv.Itoa(len(content)))
	controller := http.NewResponseController(response)
	if controller.SetWriteDeadline(time.Now().Add(30*time.Second)) == nil {
		defer controller.SetWriteDeadline(time.Time{})
	}
	_, _ = response.Write(content)
}

func (handler *Handler) deleteFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("file_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM openai_files WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "File could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "File not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "object": "file", "deleted": true})
}

func (handler *Handler) listFiles(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.filePrincipal(response, request)
	if !ok {
		return
	}
	limit, order, err := pageOptions(request, "desc")
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query := request.URL.Query()
	for _, name := range []string{"after", "purpose"} {
		if len(query[name]) > 1 {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", name+" must be specified once")
			return
		}
	}
	after, purpose := query.Get("after"), query.Get("purpose")
	if purpose != "" && !validFilePurpose(purpose, true) {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "purpose is invalid")
		return
	}
	predicate := `key_id=? AND expires_at>?`
	arguments := []any{principal.KeyID, time.Now().UnixMilli()}
	if purpose != "" {
		predicate += ` AND COALESCE(client_purpose,purpose)=?`
		arguments = append(arguments, purpose)
	}
	comparison := ">"
	if order == "DESC" {
		comparison = "<"
	}
	if after != "" {
		var createdAt int64
		cursorArguments := append(append([]any(nil), arguments...), after)
		err = handler.database.QueryRowContext(request.Context(), `SELECT created_at FROM openai_files WHERE `+predicate+` AND id=?`, cursorArguments...).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", "after is not a valid File cursor")
			return
		}
		if err != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Files are unavailable")
			return
		}
		predicate += ` AND (created_at ` + comparison + ` ? OR (created_at=? AND id ` + comparison + ` ?))`
		arguments = append(arguments, createdAt, createdAt, after)
	}
	arguments = append(arguments, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), `SELECT id,filename,COALESCE(client_purpose,purpose),bytes,created_at,expires_at FROM openai_files WHERE `+predicate+` ORDER BY created_at `+order+`,id `+order+` LIMIT ?`, arguments...)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Files are unavailable")
		return
	}
	defer rows.Close()
	items := make([]openAIFile, 0, limit)
	more := false
	for rows.Next() {
		var item openAIFile
		var createdAt, expiresAt int64
		if err = rows.Scan(&item.ID, &item.Filename, &item.Purpose, &item.Bytes, &createdAt, &expiresAt); err != nil {
			break
		}
		if len(items) == limit {
			more = true
			break
		}
		item.Object, item.Status, item.CreatedAt, item.ExpiresAt = "file", "processed", createdAt/1000, expiresAt/1000
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Files are unavailable")
		return
	}
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": nil, "last_id": nil}
	if len(items) > 0 {
		result["first_id"], result["last_id"] = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(response, result)
}

func fileAdditionalData(id, keyID, purpose string, size int64) []byte {
	return []byte(id + "\x00" + keyID + "\x00" + purpose + "\x00" + strconv.FormatInt(size, 10))
}

func containsLocalFileReference(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return walkLocalFileReferences(value)
}

func walkLocalFileReferences(value any) bool {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if walkLocalFileReferences(item) {
				return true
			}
		}
	case map[string]any:
		kind, _ := value["type"].(string)
		if kind == "input_file" || kind == "input_image" || kind == "computer_screenshot" {
			if id, _ := value["file_id"].(string); strings.HasPrefix(id, "file_") {
				return true
			}
		}
		if kind == "file" {
			if file, _ := value["file"].(map[string]any); file != nil {
				if id, _ := file["file_id"].(string); strings.HasPrefix(id, "file_") {
					return true
				}
			}
		}
		for _, item := range value {
			if walkLocalFileReferences(item) {
				return true
			}
		}
	}
	return false
}
