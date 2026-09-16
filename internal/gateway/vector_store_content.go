package gateway

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"unicode/utf8"
)

const maxVectorStoreContentChunkBytes = 64 << 10

type vectorStoreContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (handler *Handler) vectorStoreFileContent(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	storeID, fileID := request.PathValue("vector_store_id"), request.PathValue("file_id")
	if _, err := handler.readVectorStoreFile(request.Context(), principal.KeyID, storeID, fileID); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	} else if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file content is unavailable")
		return
	}
	_, content, err := handler.loadOpenAIFileContent(request.Context(), principal.KeyID, fileID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file content is unavailable")
		return
	}
	chunks, err := vectorStoreTextChunks(content)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"object": "list", "data": chunks})
}

func vectorStoreTextChunks(content []byte) ([]vectorStoreContent, error) {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("parsed content currently supports UTF-8 text files")
	}
	chunks := make([]vectorStoreContent, 0, (len(content)+maxVectorStoreContentChunkBytes-1)/maxVectorStoreContentChunkBytes)
	for len(content) > 0 {
		end := min(len(content), maxVectorStoreContentChunkBytes)
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if end < len(content) {
			if newline := bytes.LastIndexByte(content[:end], '\n'); newline >= end/2 {
				end = newline + 1
			}
		}
		chunks = append(chunks, vectorStoreContent{Type: "text", Text: string(content[:end])})
		content = content[end:]
	}
	return chunks, nil
}
