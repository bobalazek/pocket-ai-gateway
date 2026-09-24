package gateway

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type geminiReferenceError struct {
	status  int
	message string
}

type geminiFileReferenceContextKey struct{}

func (failure geminiReferenceError) Error() string { return failure.message }

// Local File URIs are expanded before routing and admission. Upstreams never see
// a gateway-owned URI, and the expanded request consumes the normal 16 MiB cap.
func (handler *Handler) resolveGeminiFileReferences(ctx context.Context, keyID, operation string, allowFiles bool, body []byte) ([]byte, error) {
	if !uniqueJSONFields(body, 64) {
		return nil, geminiReferenceError{http.StatusBadRequest, "Invalid, duplicate, or excessively nested JSON fields"}
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, geminiReferenceError{http.StatusBadRequest, "Request body must be a JSON object"}
	}
	hasFileData, scanErr := scanGeminiFileData(root)
	if scanErr != nil {
		return nil, geminiReferenceError{http.StatusBadRequest, scanErr.Error()}
	}
	if !allowFiles && hasFileData {
		return nil, geminiReferenceError{http.StatusForbidden, "Files access is not permitted"}
	}
	supported := operation == "generateContent" || operation == "streamGenerateContent" || operation == "countTokens"
	changed, references, aggregateBytes, transferHeld := false, 0, 0, false
	defer func() {
		if transferHeld {
			handler.releaseFileTransfer()
		}
	}()
	if supported {
		if contents, ok := root["contents"].([]any); ok {
			for _, content := range contents {
				object, ok := content.(map[string]any)
				if !ok {
					continue
				}
				parts, ok := object["parts"].([]any)
				if !ok {
					continue
				}
				for _, value := range parts {
					part, ok := value.(map[string]any)
					if !ok {
						continue
					}
					file, hasCamel := part["fileData"]
					if !hasCamel {
						file, _ = part["file_data"]
					}
					if file == nil {
						continue
					}
					if len(part) != 1 || references >= 4 {
						return nil, geminiReferenceError{http.StatusBadRequest, "File parts must be standalone; at most four file references are supported"}
					}
					data, ok := file.(map[string]any)
					if !ok {
						return nil, geminiReferenceError{http.StatusBadRequest, "Invalid file_data part"}
					}
					for key := range data {
						if key != "fileUri" && key != "file_uri" && key != "mimeType" && key != "mime_type" {
							return nil, geminiReferenceError{http.StatusBadRequest, "Unsupported file_data field"}
						}
					}
					if data["fileUri"] != nil && data["file_uri"] != nil || data["mimeType"] != nil && data["mime_type"] != nil {
						return nil, geminiReferenceError{http.StatusBadRequest, "Duplicate file_data field"}
					}
					uri, _ := data["fileUri"].(string)
					if uri == "" {
						uri, _ = data["file_uri"].(string)
					}
					id := strings.TrimPrefix(uri, "pag-gemini://files/")
					if !validGeminiFileID(id) || geminiFileURI(id) != uri {
						return nil, geminiReferenceError{http.StatusBadRequest, "Only gateway-owned Gemini File URIs are supported"}
					}
					if !transferHeld {
						select {
						case handler.fileTransfers <- struct{}{}:
							transferHeld = true
						default:
							return nil, geminiReferenceError{http.StatusTooManyRequests, "Another file transfer is in progress"}
						}
					}
					item, content, err := handler.readGeminiFile(ctx, keyID, id)
					if errors.Is(err, sql.ErrNoRows) {
						return nil, geminiReferenceError{http.StatusNotFound, "File not found"}
					}
					if err != nil {
						return nil, geminiReferenceError{http.StatusServiceUnavailable, "File is unavailable"}
					}
					mimeType, _ := data["mimeType"].(string)
					if mimeType == "" {
						mimeType, _ = data["mime_type"].(string)
					}
					if mimeType != "" {
						baseMIME, valid := geminiBaseMIME(mimeType)
						if !valid || baseMIME != item.MIMEType {
							return nil, geminiReferenceError{http.StatusBadRequest, "File MIME type does not match the uploaded file"}
						}
					}
					if len(content) > maxGeminiFileBytes-aggregateBytes {
						return nil, geminiReferenceError{http.StatusRequestEntityTooLarge, "Referenced files exceed 8 MiB combined"}
					}
					aggregateBytes += len(content)
					field, mimeField := "inlineData", "mimeType"
					if !hasCamel {
						field, mimeField = "inline_data", "mime_type"
					}
					delete(part, "fileData")
					delete(part, "file_data")
					part[field] = map[string]any{mimeField: item.MIMEType, "data": base64.StdEncoding.EncodeToString(content)}
					changed, references = true, references+1
				}
			}
		}
	}
	if hasFileData, scanErr = scanGeminiFileData(root); scanErr != nil {
		return nil, geminiReferenceError{http.StatusBadRequest, scanErr.Error()}
	} else if hasFileData {
		return nil, geminiReferenceError{http.StatusBadRequest, "File references are supported only in generateContent and countTokens contents parts"}
	}
	if !changed {
		return body, nil
	}
	expanded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode Gemini file references: %w", err)
	}
	if len(expanded) > maxInferenceBody {
		return nil, geminiReferenceError{http.StatusRequestEntityTooLarge, "Expanded request exceeds 16 MiB"}
	}
	return expanded, nil
}

func scanGeminiFileData(value any) (bool, error) {
	type item struct {
		value any
		depth int
	}
	stack := []item{{value: value}}
	found := false
	for seen := 0; len(stack) > 0; seen++ {
		if seen >= 100000 {
			return false, errors.New("request contains too many JSON values")
		}
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current.depth > 64 {
			return false, errors.New("request nesting exceeds 64 levels")
		}
		switch nested := current.value.(type) {
		case map[string]any:
			for key, child := range nested {
				if key == "fileData" || key == "file_data" {
					found = true
				}
				stack = append(stack, item{value: child, depth: current.depth + 1})
			}
		case []any:
			for _, child := range nested {
				stack = append(stack, item{value: child, depth: current.depth + 1})
			}
		}
	}
	return found, nil
}
