package gateway

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	maxVectorStoreContentChunkBytes = 64 << 10
	maxVectorStoreDOCXXMLBytes      = 16 << 20
	maxVectorStoreDOCXEntries       = 1024
)

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
	file, content, err := handler.loadOpenAIFileContent(request.Context(), principal.KeyID, fileID)
	if errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store file not found")
		return
	}
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store file content is unavailable")
		return
	}
	chunks, err := vectorStoreContentChunks(file.Filename, content)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", err.Error())
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"object": "list", "data": chunks})
}

func vectorStoreContentChunks(filename string, content []byte) ([]vectorStoreContent, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".docx") {
		text, err := vectorStoreDOCXText(content)
		if err != nil {
			return nil, err
		}
		content = text
	}
	return vectorStoreTextChunks(content)
}

func vectorStoreDOCXText(content []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) > maxVectorStoreDOCXEntries {
		return nil, errors.New("DOCX content is invalid or exceeds the archive entry limit")
	}
	var document *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			if document != nil {
				return nil, errors.New("DOCX contains duplicate body documents")
			}
			document = file
		}
	}
	if document == nil || document.UncompressedSize64 > maxVectorStoreDOCXXMLBytes {
		return nil, errors.New("DOCX body document is missing or exceeds 16 MiB")
	}
	reader, err := document.Open()
	if err != nil {
		return nil, errors.New("DOCX body document could not be opened")
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxVectorStoreDOCXXMLBytes+1))
	var text strings.Builder
	lastNewline := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			break
		}
		if tokenErr != nil {
			return nil, errors.New("DOCX body document is not valid XML")
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch {
			case vectorStoreWordElement(value.Name, "t"):
				var valueText string
				if decoder.DecodeElement(&valueText, &value) != nil || text.Len()+len(valueText) > maxVectorStoreDOCXXMLBytes {
					return nil, errors.New("DOCX text is invalid or exceeds 16 MiB")
				}
				text.WriteString(valueText)
				lastNewline = strings.HasSuffix(valueText, "\n")
			case vectorStoreWordElement(value.Name, "tab"):
				text.WriteByte('\t')
				lastNewline = false
			case vectorStoreWordElement(value.Name, "br"), vectorStoreWordElement(value.Name, "cr"):
				text.WriteByte('\n')
				lastNewline = true
			}
		case xml.EndElement:
			if vectorStoreWordElement(value.Name, "p") && text.Len() > 0 && !lastNewline {
				text.WriteByte('\n')
				lastNewline = true
			}
		}
		if text.Len() > maxVectorStoreDOCXXMLBytes {
			return nil, errors.New("DOCX text exceeds 16 MiB")
		}
	}
	return []byte(text.String()), nil
}

func vectorStoreWordElement(name xml.Name, local string) bool {
	return name.Local == local && (name.Space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" || name.Space == "http://purl.oclc.org/ooxml/wordprocessingml/main")
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
