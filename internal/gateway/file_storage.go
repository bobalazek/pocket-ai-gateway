package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

func (handler *Handler) acquireFileTransfer(response http.ResponseWriter) bool {
	select {
	case handler.fileTransfers <- struct{}{}:
		return true
	default:
		handler.writeError(response, "openai", http.StatusTooManyRequests, "rate_limit_exceeded", "Another file operation is already in progress")
		return false
	}
}

func (handler *Handler) releaseFileTransfer() { <-handler.fileTransfers }

func (handler *Handler) loadOpenAIFileContent(ctx context.Context, keyID, id string) (openAIFile, []byte, error) {
	var item openAIFile
	var createdAt, expiresAt int64
	var ciphertext, nonce []byte
	err := handler.database.QueryRowContext(ctx, `SELECT id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at FROM openai_files WHERE id=? AND key_id=? AND expires_at>?`, id, keyID, time.Now().UnixMilli()).Scan(&item.ID, &item.Filename, &item.Purpose, &item.Bytes, &ciphertext, &nonce, &createdAt, &expiresAt)
	if err != nil {
		return openAIFile{}, nil, err
	}
	content, err := credentials.Open(handler.masterKey, ciphertext, nonce, fileAdditionalData(item.ID, keyID, item.Purpose, item.Bytes))
	if err != nil {
		return openAIFile{}, nil, err
	}
	item.Object, item.Status, item.CreatedAt, item.ExpiresAt = "file", "processed", createdAt/1000, expiresAt/1000
	return item, content, nil
}

func (handler *Handler) insertGeneratedOpenAIFile(ctx context.Context, tx *sql.Tx, ownerID, keyID, filename string, content []byte, expiresAt time.Time) (openAIFile, error) {
	token, err := credentials.RandomToken(18)
	if err != nil {
		return openAIFile{}, err
	}
	id := "file_" + token
	ciphertext, nonce, err := credentials.Seal(handler.masterKey, content, fileAdditionalData(id, keyID, "batch_output", int64(len(content))))
	if err != nil {
		return openAIFile{}, err
	}
	now := time.Now()
	if minimum := now.Add(time.Hour); expiresAt.Before(minimum) {
		expiresAt = minimum
	}
	if maximum := now.Add(defaultFileExpiry); expiresAt.After(maximum) {
		expiresAt = maximum
	}
	item := openAIFile{ID: id, Object: "file", Bytes: int64(len(content)), CreatedAt: now.Unix(), ExpiresAt: expiresAt.Unix(), Filename: filename, Purpose: "batch_output", Status: "processed"}
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, ownerID, keyID, item.Filename, item.Purpose, item.Bytes, ciphertext, nonce, now.UnixMilli(), expiresAt.UnixMilli())
	return item, err
}
