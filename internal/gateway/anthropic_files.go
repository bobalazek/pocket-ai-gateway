package gateway

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const (
	maxAnthropicFileBytes      = 8 << 20 // Inline references must fit the 16 MiB Messages limit.
	maxAnthropicUploadBody     = 9 << 20
	maxAnthropicFileReferences = 32
	defaultAnthropicFileExpiry = 30 * 24 * time.Hour
)

type anthropicFile struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
	Downloadable bool   `json:"downloadable"`
}

var (
	errAnthropicFileUnavailable  = errors.New("file not found")
	errAnthropicFileForbidden    = errors.New("files access is not permitted")
	errAnthropicFileStorage      = errors.New("file content is unavailable")
	errAnthropicExpandedTooLarge = errors.New("expanded Messages request exceeds 16 MiB")
	errAnthropicFileBusy         = errors.New("another file operation is already in progress")
)

func (handler *Handler) anthropicFilePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	if strings.Contains(request.Header.Get("anthropic-beta"), "files-api-2025-04-14") {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Legacy Files beta shapes are not supported; use the current Files API")
		return keys.Principal{}, false
	}
	principal, ok := handler.authenticate(response, request, "anthropic")
	if ok && !principalHasScope(principal.Scopes, "files:manage") {
		handler.writeError(response, "anthropic", http.StatusForbidden, "permission_error", "Files access is not permitted")
		return keys.Principal{}, false
	}
	return principal, ok
}

func (handler *Handler) createAnthropicFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.anthropicFilePrincipal(response, request)
	if !ok || !handler.acquireMultipart(response, "anthropic") {
		return
	}
	defer handler.releaseMultipart()
	if request.ContentLength > maxAnthropicUploadBody {
		handler.writeError(response, "anthropic", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 9 MiB")
		return
	}
	limited := http.MaxBytesReader(response, request.Body, maxAnthropicUploadBody)
	filename, mimeType, content, expiry, err := parseAnthropicUpload(limited, request.Header.Get("Content-Type"))
	if err == nil {
		_, err = io.Copy(io.Discard, limited)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, errFileTooLarge) {
			handler.writeError(response, "anthropic", http.StatusRequestEntityTooLarge, "request_too_large", "File exceeds the gateway upload limit")
		} else {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", err.Error())
		}
		return
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File could not be created")
		return
	}
	now := time.Now().UTC()
	item := anthropicFile{ID: "file_" + token, Type: "file", Filename: filename, MimeType: mimeType, SizeBytes: int64(len(content)), CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(expiry).Format(time.RFC3339Nano)}
	ciphertext, nonce, err := credentials.Seal(handler.masterKey, content, anthropicFileAAD(item.ID, principal.KeyID, item.MimeType, item.SizeBytes))
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File could not be created")
		return
	}
	tx, err := handler.database.BeginTx(request.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = checkRetainedResourceCapacity(request.Context(), tx, principal.OwnerUserID, principal.KeyID, 1, int64(len(filename)+len(mimeType)+len(ciphertext)+len(nonce)))
	}
	if err == nil {
		_, err = tx.ExecContext(request.Context(), `INSERT INTO anthropic_files(id,owner_user_id,key_id,filename,mime_type,size_bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, principal.OwnerUserID, principal.KeyID, filename, mimeType, item.SizeBytes, ciphertext, nonce, now.UnixMilli(), now.Add(expiry).UnixMilli())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, errRetainedResourceLimit) {
			handler.writeError(response, "anthropic", http.StatusTooManyRequests, "rate_limit_error", "Stored inference resource retention limit reached")
		} else {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File could not be created")
		}
		return
	}
	writeJSON(response, item)
}

func parseAnthropicUpload(body io.Reader, contentType string) (string, string, []byte, time.Duration, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return "", "", nil, 0, errors.New("content type must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(body, params["boundary"])
	expiry := defaultAnthropicFileExpiry
	var filename, fileType string
	var content []byte
	seen := map[string]bool{}
	for parts := 0; ; parts++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", nil, 0, err
		}
		if parts >= 2 || seen[part.FormName()] {
			return "", "", nil, 0, errors.New("multipart request has duplicate or extra fields")
		}
		seen[part.FormName()] = true
		switch part.FormName() {
		case "file":
			filename = part.FileName()
			if len(filename) > 255 || !validFileName(filename) || strings.ContainsAny(filename, `<>:"|?*`) {
				return "", "", nil, 0, errors.New("file must have a safe filename of at most 255 bytes")
			}
			content, err = io.ReadAll(io.LimitReader(part, maxAnthropicFileBytes+1))
			if err != nil {
				return "", "", nil, 0, err
			}
			if len(content) > maxAnthropicFileBytes {
				return "", "", nil, 0, errFileTooLarge
			}
			if len(content) == 0 {
				return "", "", nil, 0, errors.New("file must be non-empty")
			}
			fileType = part.Header.Get("Content-Type")
		case "expires_in_seconds":
			if part.FileName() != "" {
				return "", "", nil, 0, errors.New("expires_in_seconds must be a number")
			}
			value, err := io.ReadAll(io.LimitReader(part, 32))
			if err != nil || len(value) > 16 {
				return "", "", nil, 0, errors.New("expires_in_seconds is invalid")
			}
			seconds, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil || seconds < 3600 || seconds > 7776000 {
				return "", "", nil, 0, errors.New("expires_in_seconds must be between 3600 and 7776000")
			}
			expiry = time.Duration(seconds) * time.Second
		default:
			return "", "", nil, 0, errors.New("multipart fields must be file or expires_in_seconds")
		}
	}
	if !seen["file"] {
		return "", "", nil, 0, errors.New("file is required")
	}
	if fileType != "" {
		fileType, _, err = mime.ParseMediaType(fileType)
		if err != nil {
			return "", "", nil, 0, errors.New("file content type is invalid")
		}
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(content))
	if fileType == "" || fileType == "application/octet-stream" {
		fileType = detected
	}
	if fileType == "text/plain" {
		if !utf8.Valid(content) {
			return "", "", nil, 0, errors.New("text files must contain UTF-8")
		}
	} else if fileType != detected {
		return "", "", nil, 0, errors.New("file content does not match its content type")
	}
	switch fileType {
	case "application/pdf", "text/plain", "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", "", nil, 0, errors.New("only PDF, UTF-8 text, and supported images can be referenced")
	}
	return filename, fileType, content, expiry, nil
}

func anthropicFileAAD(id, keyID, mimeType string, size int64) []byte {
	return []byte("anthropic-file\x00" + id + "\x00" + keyID + "\x00" + mimeType + "\x00" + strconv.FormatInt(size, 10))
}

func (handler *Handler) readAnthropicFile(ctx context.Context, keyID, id string, withContent bool) (anthropicFile, []byte, error) {
	var item anthropicFile
	var created, expires int64
	var ciphertext, nonce []byte
	query := `SELECT id,filename,mime_type,size_bytes,created_at,expires_at FROM anthropic_files WHERE id=? AND key_id=? AND expires_at>?`
	if withContent {
		query = `SELECT id,filename,mime_type,size_bytes,created_at,expires_at,ciphertext,nonce FROM anthropic_files WHERE id=? AND key_id=? AND expires_at>?`
	}
	row := handler.database.QueryRowContext(ctx, query, id, keyID, time.Now().UnixMilli())
	var err error
	if withContent {
		err = row.Scan(&item.ID, &item.Filename, &item.MimeType, &item.SizeBytes, &created, &expires, &ciphertext, &nonce)
	} else {
		err = row.Scan(&item.ID, &item.Filename, &item.MimeType, &item.SizeBytes, &created, &expires)
	}
	if err != nil {
		return anthropicFile{}, nil, err
	}
	item.Type = "file"
	item.CreatedAt = time.UnixMilli(created).UTC().Format(time.RFC3339Nano)
	item.ExpiresAt = time.UnixMilli(expires).UTC().Format(time.RFC3339Nano)
	if !withContent {
		return item, nil, nil
	}
	content, err := credentials.Open(handler.masterKey, ciphertext, nonce, anthropicFileAAD(item.ID, keyID, item.MimeType, item.SizeBytes))
	return item, content, err
}

func (handler *Handler) getAnthropicFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.anthropicFilePrincipal(response, request)
	if !ok {
		return
	}
	item, _, err := handler.readAnthropicFile(request.Context(), principal.KeyID, request.PathValue("file_id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "File not found")
	} else if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File is unavailable")
	} else {
		writeJSON(response, item)
	}
}

func (handler *Handler) deleteAnthropicFile(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.anthropicFilePrincipal(response, request)
	if !ok {
		return
	}
	id := request.PathValue("file_id")
	result, err := handler.database.ExecContext(request.Context(), `DELETE FROM anthropic_files WHERE id=? AND key_id=? AND expires_at>?`, id, principal.KeyID, time.Now().UnixMilli())
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File could not be deleted")
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "File not found")
		return
	}
	writeJSON(response, map[string]any{"id": id, "type": "file_deleted"})
}

func (handler *Handler) anthropicFileContent(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.anthropicFilePrincipal(response, request)
	if !ok {
		return
	}
	if _, _, err := handler.readAnthropicFile(request.Context(), principal.KeyID, request.PathValue("file_id"), false); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "anthropic", http.StatusNotFound, "not_found_error", "File not found")
	} else if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "File is unavailable")
	} else {
		handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Uploaded files are not downloadable")
	}
}

func (handler *Handler) listAnthropicFiles(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.anthropicFilePrincipal(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key, values := range query {
		if key != "limit" && key != "page" {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "Unsupported Files list filter")
			return
		}
		if len(values) != 1 {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "List parameters must be specified once")
			return
		}
	}
	limit := 20
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > 100 {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	predicate := `key_id=? AND expires_at>?`
	args := []any{principal.KeyID, time.Now().UnixMilli()}
	if query.Has("page") {
		cursor := strings.TrimPrefix(query.Get("page"), "page_")
		if cursor == query.Get("page") || cursor == "" || len(cursor) > 80 {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "page is invalid")
			return
		}
		var created int64
		err := handler.database.QueryRowContext(request.Context(), `SELECT created_at FROM anthropic_files WHERE id=? AND key_id=? AND expires_at>?`, cursor, principal.KeyID, time.Now().UnixMilli()).Scan(&created)
		if errors.Is(err, sql.ErrNoRows) {
			handler.writeError(response, "anthropic", http.StatusBadRequest, "invalid_request_error", "page is invalid")
			return
		}
		if err != nil {
			handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "Files are unavailable")
			return
		}
		predicate += ` AND (created_at<? OR (created_at=? AND id<?))`
		args = append(args, created, created, cursor)
	}
	args = append(args, limit+1)
	rows, err := handler.database.QueryContext(request.Context(), `SELECT id,filename,mime_type,size_bytes,created_at,expires_at FROM anthropic_files WHERE `+predicate+` ORDER BY created_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "Files are unavailable")
		return
	}
	defer rows.Close()
	items := make([]anthropicFile, 0, limit)
	next := (*string)(nil)
	for rows.Next() {
		var item anthropicFile
		var created, expires int64
		if err = rows.Scan(&item.ID, &item.Filename, &item.MimeType, &item.SizeBytes, &created, &expires); err != nil {
			break
		}
		if len(items) == limit {
			page := "page_" + items[len(items)-1].ID
			next = &page
			break
		}
		item.Type = "file"
		item.CreatedAt = time.UnixMilli(created).UTC().Format(time.RFC3339Nano)
		item.ExpiresAt = time.UnixMilli(expires).UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		handler.writeError(response, "anthropic", http.StatusServiceUnavailable, "api_error", "Files are unavailable")
		return
	}
	writeJSON(response, map[string]any{"data": items, "next_page": next})
}

func (handler *Handler) expandAnthropicFileReferences(ctx context.Context, keyID string, canUseFiles bool, originalBodyBytes int, envelope map[string]json.RawMessage) ([]byte, error) {
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["messages"], &messages); err != nil || messages == nil {
		return nil, nil // Existing Messages validation owns malformed requests.
	}
	changed := false
	locked := false
	references := 0
	estimatedExpandedBytes := originalBodyBytes
	for _, message := range messages {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			if kind == "container_upload" {
				return nil, errors.New("container_upload files are not supported")
			}
			var source map[string]json.RawMessage
			if json.Unmarshal(block["source"], &source) != nil {
				continue
			}
			var sourceType string
			_ = json.Unmarshal(source["type"], &sourceType)
			if sourceType != "file" {
				continue
			}
			references++
			if references > maxAnthropicFileReferences {
				return nil, errors.New("messages request has too many file references")
			}
			if kind != "document" && kind != "image" {
				return nil, errors.New("file source requires a document or image block")
			}
			if !canUseFiles {
				return nil, errAnthropicFileForbidden
			}
			if !locked {
				select {
				case handler.fileTransfers <- struct{}{}:
					defer handler.releaseFileTransfer()
					locked = true
				default:
					return nil, errAnthropicFileBusy
				}
			}
			var id string
			if json.Unmarshal(source["file_id"], &id) != nil || id == "" {
				return nil, errors.New("file source requires file_id")
			}
			item, content, err := handler.readAnthropicFile(ctx, keyID, id, true)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errAnthropicFileUnavailable
			}
			if err != nil {
				return nil, errAnthropicFileStorage
			}
			if kind == "document" && item.MimeType != "application/pdf" && item.MimeType != "text/plain" || kind == "image" && !strings.HasPrefix(item.MimeType, "image/") {
				return nil, errors.New("file type does not match content block")
			}
			inlineBytes := base64.StdEncoding.EncodedLen(len(content))
			if item.MimeType == "text/plain" {
				inlineBytes = len(content)
			}
			// Charge the whole inline payload before encoding. This conservative estimate
			// bounds repeated IDs and avoids allocating a large intermediate request.
			if inlineBytes > maxInferenceBody-estimatedExpandedBytes {
				return nil, errAnthropicExpandedTooLarge
			}
			estimatedExpandedBytes += inlineBytes
			var data string
			if item.MimeType == "text/plain" {
				sourceType, data = "text", string(content)
			} else {
				sourceType, data = "base64", base64.StdEncoding.EncodeToString(content)
			}
			source = make(map[string]json.RawMessage, 3)
			source["type"], _ = json.Marshal(sourceType)
			source["media_type"], _ = json.Marshal(item.MimeType)
			source["data"], _ = json.Marshal(data)
			block["source"], _ = json.Marshal(source)
			changed = true
		}
		if changed {
			message["content"], _ = json.Marshal(blocks)
		}
	}
	if !changed {
		return nil, nil
	}
	envelope["messages"], _ = json.Marshal(messages)
	body, err := json.Marshal(envelope)
	if err != nil || len(body) > maxInferenceBody {
		return nil, errAnthropicExpandedTooLarge
	}
	return body, nil
}
