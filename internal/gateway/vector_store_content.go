package gateway

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxVectorStoreContentChunkBytes  = 64 << 10
	maxVectorStoreParsedContentBytes = 16 << 20
	maxVectorStoreOOXMLEntries       = 1024
)

const (
	wordprocessingMLNamespace       = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	strictWordprocessingMLNamespace = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	presentationMLNamespace         = "http://schemas.openxmlformats.org/presentationml/2006/main"
	strictPresentationMLNamespace   = "http://purl.oclc.org/ooxml/presentationml/main"
	drawingMLNamespace              = "http://schemas.openxmlformats.org/drawingml/2006/main"
	strictDrawingMLNamespace        = "http://purl.oclc.org/ooxml/drawingml/main"
	officeRelationshipNamespace     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	strictOfficeRelationshipNS      = "http://purl.oclc.org/ooxml/officeDocument/relationships"
	packageRelationshipNamespace    = "http://schemas.openxmlformats.org/package/2006/relationships"
	strictPackageRelationshipNS     = "http://purl.oclc.org/ooxml/package/relationships"
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
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".docx"):
		text, err := vectorStoreDOCXText(content)
		if err != nil {
			return nil, err
		}
		content = text
	case strings.HasSuffix(strings.ToLower(filename), ".pptx"):
		text, err := vectorStorePPTXText(content)
		if err != nil {
			return nil, err
		}
		content = text
	case strings.HasSuffix(strings.ToLower(filename), ".xlsx"):
		text, err := vectorStoreXLSXText(content)
		if err != nil {
			return nil, err
		}
		content = text
	case strings.HasSuffix(strings.ToLower(filename), ".html"):
		text, err := vectorStoreHTMLText(content)
		if err != nil {
			return nil, err
		}
		content = text
	}
	return vectorStoreTextChunks(content)
}

func vectorStoreDOCXText(content []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) > maxVectorStoreOOXMLEntries {
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
	if document == nil || document.UncompressedSize64 > maxVectorStoreParsedContentBytes {
		return nil, errors.New("DOCX body document is missing or exceeds 16 MiB")
	}
	return vectorStoreOOXMLPartText(document, "DOCX", "document", wordprocessingMLNamespace, strictWordprocessingMLNamespace, wordprocessingMLNamespace, strictWordprocessingMLNamespace, maxVectorStoreParsedContentBytes)
}

func vectorStorePPTXText(content []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) > maxVectorStoreOOXMLEntries {
		return nil, errors.New("PPTX content is invalid or exceeds the archive entry limit")
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if files[file.Name] != nil {
			return nil, errors.New("PPTX contains duplicate package parts")
		}
		files[file.Name] = file
	}
	presentation, relationships := files["ppt/presentation.xml"], files["ppt/_rels/presentation.xml.rels"]
	if presentation == nil || relationships == nil || presentation.UncompressedSize64 > maxVectorStoreParsedContentBytes || relationships.UncompressedSize64 > maxVectorStoreParsedContentBytes {
		return nil, errors.New("PPTX presentation metadata is missing or exceeds 16 MiB")
	}
	targets, err := vectorStorePPTXRelationshipTargets(relationships)
	if err != nil {
		return nil, err
	}
	slideNames, err := vectorStorePPTXSlideNames(presentation, targets)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var declared uint64
	seen := make(map[string]struct{}, len(slideNames))
	for _, name := range slideNames {
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("PPTX contains a duplicate slide reference")
		}
		seen[name] = struct{}{}
		slide := files[name]
		if slide == nil || slide.UncompressedSize64 > uint64(maxVectorStoreParsedContentBytes)-declared {
			return nil, errors.New("PPTX slide content is missing or exceeds 16 MiB")
		}
		declared += slide.UncompressedSize64
		slideText, partErr := vectorStoreOOXMLPartText(slide, "PPTX", "sld", presentationMLNamespace, strictPresentationMLNamespace, drawingMLNamespace, strictDrawingMLNamespace, maxVectorStoreParsedContentBytes-text.Len())
		if partErr != nil {
			return nil, partErr
		}
		text.Write(slideText)
	}
	return []byte(text.String()), nil
}

func vectorStorePPTXRelationshipTargets(file *zip.File) (map[string]string, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New("PPTX relationships could not be opened")
	}
	defer closePart()
	targets := make(map[string]string)
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, errors.New("PPTX relationships root is invalid")
			}
			return targets, nil
		}
		if tokenErr != nil {
			return nil, errors.New("PPTX relationships are not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if start.Name.Local != "Relationships" || (start.Name.Space != packageRelationshipNamespace && start.Name.Space != strictPackageRelationshipNS) {
				return nil, errors.New("PPTX relationships root is invalid")
			}
			rootSeen = true
			continue
		}
		if start.Name.Local != "Relationship" || (start.Name.Space != packageRelationshipNamespace && start.Name.Space != strictPackageRelationshipNS) {
			continue
		}
		var id, target, relationshipType, targetMode string
		for _, attribute := range start.Attr {
			switch attribute.Name.Local {
			case "Id":
				id = attribute.Value
			case "Target":
				target = attribute.Value
			case "Type":
				relationshipType = attribute.Value
			case "TargetMode":
				targetMode = attribute.Value
			}
		}
		if relationshipType != officeRelationshipNamespace+"/slide" && relationshipType != strictOfficeRelationshipNS+"/slide" {
			continue
		}
		if id == "" || target == "" || (targetMode != "" && targetMode != "Internal") || strings.Contains(target, "\\") {
			return nil, errors.New("PPTX contains an invalid slide relationship")
		}
		name := path.Clean(path.Join("ppt", strings.TrimPrefix(target, "/ppt/")))
		if !strings.HasPrefix(name, "ppt/slides/") || path.Ext(name) != ".xml" || targets[id] != "" {
			return nil, errors.New("PPTX contains an invalid slide relationship")
		}
		targets[id] = name
	}
}

func vectorStorePPTXSlideNames(file *zip.File, targets map[string]string) ([]string, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New("PPTX presentation could not be opened")
	}
	defer closePart()
	var slides []string
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, errors.New("PPTX presentation root is invalid")
			}
			return slides, nil
		}
		if tokenErr != nil {
			return nil, errors.New("PPTX presentation is not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if start.Name.Local != "presentation" || (start.Name.Space != presentationMLNamespace && start.Name.Space != strictPresentationMLNamespace) {
				return nil, errors.New("PPTX presentation root is invalid")
			}
			rootSeen = true
			continue
		}
		if start.Name.Local != "sldId" || (start.Name.Space != presentationMLNamespace && start.Name.Space != strictPresentationMLNamespace) {
			continue
		}
		var relationshipID string
		for _, attribute := range start.Attr {
			if attribute.Name.Local == "id" && (attribute.Name.Space == officeRelationshipNamespace || attribute.Name.Space == strictOfficeRelationshipNS) {
				relationshipID = attribute.Value
			}
		}
		if targets[relationshipID] == "" {
			return nil, errors.New("PPTX slide order references a missing relationship")
		}
		slides = append(slides, targets[relationshipID])
	}
}

func vectorStoreOOXMLDecoder(file *zip.File) (*xml.Decoder, func(), error) {
	reader, err := file.Open()
	if err != nil {
		return nil, nil, err
	}
	return xml.NewDecoder(io.LimitReader(reader, int64(file.UncompressedSize64)+1)), func() { _ = reader.Close() }, nil
}

func vectorStoreOOXMLPartText(file *zip.File, format, rootLocal, rootNamespace, strictRootNamespace, textNamespace, strictTextNamespace string, maxText int) ([]byte, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New(format + " document part could not be opened")
	}
	defer closePart()
	var text strings.Builder
	lastNewline := false
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, errors.New(format + " document root is invalid")
			}
			break
		}
		if tokenErr != nil {
			return nil, errors.New(format + " document part is not valid XML")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if !rootSeen {
				if value.Name.Local != rootLocal || (value.Name.Space != rootNamespace && value.Name.Space != strictRootNamespace) {
					return nil, errors.New(format + " document root is invalid")
				}
				rootSeen = true
				continue
			}
			switch {
			case vectorStoreOOXMLElement(value.Name, "t", textNamespace, strictTextNamespace):
				var valueText string
				if decoder.DecodeElement(&valueText, &value) != nil || text.Len()+len(valueText) > maxText {
					return nil, errors.New(format + " text is invalid or exceeds 16 MiB")
				}
				text.WriteString(valueText)
				lastNewline = strings.HasSuffix(valueText, "\n")
			case vectorStoreOOXMLElement(value.Name, "tab", textNamespace, strictTextNamespace):
				text.WriteByte('\t')
				lastNewline = false
			case vectorStoreOOXMLElement(value.Name, "br", textNamespace, strictTextNamespace), vectorStoreOOXMLElement(value.Name, "cr", textNamespace, strictTextNamespace):
				text.WriteByte('\n')
				lastNewline = true
			}
		case xml.EndElement:
			if vectorStoreOOXMLElement(value.Name, "p", textNamespace, strictTextNamespace) && text.Len() > 0 && !lastNewline {
				text.WriteByte('\n')
				lastNewline = true
			}
		}
		if text.Len() > maxText {
			return nil, errors.New(format + " text exceeds 16 MiB")
		}
	}
	return []byte(text.String()), nil
}

func vectorStoreOOXMLElement(name xml.Name, local, namespace, strictNamespace string) bool {
	return name.Local == local && (name.Space == namespace || name.Space == strictNamespace)
}

func vectorStoreTextChunks(content []byte) ([]vectorStoreContent, error) {
	content, err := vectorStoreUTF8Text(content)
	if err != nil {
		return nil, err
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

func vectorStoreUTF8Text(content []byte) ([]byte, error) {
	if bytes.HasPrefix(content, []byte{0xff, 0xfe}) || bytes.HasPrefix(content, []byte{0xfe, 0xff}) {
		if len(content)%2 != 0 {
			return nil, errors.New("UTF-16 text has an incomplete code unit")
		}
		var order binary.ByteOrder = binary.BigEndian
		if content[0] == 0xff {
			order = binary.LittleEndian
		}
		var text strings.Builder
		text.Grow(min(len(content), maxVectorStoreParsedContentBytes))
		for offset := 2; offset < len(content); offset += 2 {
			unit := order.Uint16(content[offset : offset+2])
			character := rune(unit)
			if utf16.IsSurrogate(character) {
				if unit < 0xd800 || unit > 0xdbff || offset+3 >= len(content) {
					return nil, errors.New("UTF-16 text contains an invalid surrogate pair")
				}
				next := order.Uint16(content[offset+2 : offset+4])
				if next < 0xdc00 || next > 0xdfff {
					return nil, errors.New("UTF-16 text contains an invalid surrogate pair")
				}
				character = utf16.DecodeRune(character, rune(next))
				offset += 2
			}
			if character == 0 {
				return nil, errors.New("parsed text cannot contain NUL characters")
			}
			if text.Len()+utf8.RuneLen(character) > maxVectorStoreParsedContentBytes {
				return nil, errors.New("parsed text exceeds 16 MiB")
			}
			text.WriteRune(character)
		}
		return []byte(text.String()), nil
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(content) {
		return nil, errors.New("parsed text must be UTF-8 or BOM-marked UTF-16")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("parsed text cannot contain NUL characters")
	}
	return content, nil
}
