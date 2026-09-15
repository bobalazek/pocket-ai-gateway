package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const maxConversationStreamFrames = 131072

var (
	errConversationRequest         = errors.New("invalid conversation request")
	errConversationContextTooLarge = errors.New("conversation context exceeds request limit")
	errConversationChanged         = errors.New("conversation changed")
)

type conversationAttachment struct {
	id, keyID, ownerID string
	revision           int64
	newItems           []json.RawMessage
	outputItems        []json.RawMessage
	requestBody        []byte
	deferStore         bool
}

type conversationAttachmentContextKey struct{}

func responseConversationID(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return "", nil
	}
	var id string
	if json.Unmarshal(trimmed, &id) == nil && id != "" {
		return id, nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(trimmed, &value) != nil || len(value) != 1 || json.Unmarshal(value["id"], &id) != nil || id == "" {
		return "", errors.New("conversation must be a conversation ID or an object containing only id")
	}
	return id, nil
}

func (handler *Handler) prepareConversationResponse(ctx context.Context, principal keys.Principal, envelope map[string]json.RawMessage) (conversationAttachment, []byte, error) {
	id, err := responseConversationID(envelope["conversation"])
	if err != nil || id == "" {
		return conversationAttachment{}, nil, err
	}
	newItems, err := responseRequestConversationItems(envelope["input"])
	if err != nil {
		return conversationAttachment{}, nil, errConversationRequest
	}
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return conversationAttachment{}, nil, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", id, principal.KeyID).Scan(&revision); err != nil {
		return conversationAttachment{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body_json FROM conversation_items WHERE conversation_id=? ORDER BY ordinal", id)
	if err != nil {
		return conversationAttachment{}, nil, err
	}
	defer rows.Close()
	var history []json.RawMessage
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return conversationAttachment{}, nil, err
		}
		history = append(history, json.RawMessage(body))
	}
	if err := rows.Err(); err != nil {
		return conversationAttachment{}, nil, err
	}
	if err := rows.Close(); err != nil {
		return conversationAttachment{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return conversationAttachment{}, nil, err
	}
	storedItems := append(append([]json.RawMessage{}, history...), newItems...)
	storedInput, _ := json.Marshal(storedItems)
	envelope["input"] = storedInput
	delete(envelope, "conversation")
	storedBody, _ := json.Marshal(envelope)
	body, err := conversationDispatchBody(storedBody)
	if err == nil && len(body) > maxInferenceBody {
		err = errConversationContextTooLarge
	}
	return conversationAttachment{id: id, keyID: principal.KeyID, ownerID: principal.OwnerUserID, revision: revision, newItems: newItems, requestBody: storedBody}, body, err
}

func conversationDispatchBody(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return nil, errors.New("conversation request is invalid")
	}
	var items []json.RawMessage
	if json.Unmarshal(envelope["input"], &items) != nil {
		return nil, errors.New("conversation input is invalid")
	}
	dispatch := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var value map[string]json.RawMessage
		if json.Unmarshal(item, &value) != nil {
			return nil, errors.New("conversation contains an invalid item")
		}
		delete(value, "id")
		encoded, _ := json.Marshal(value)
		dispatch = append(dispatch, encoded)
	}
	encodedItems, _ := json.Marshal(dispatch)
	envelope["input"] = encodedItems
	return json.Marshal(envelope)
}

func responseRequestConversationItems(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []json.RawMessage{}, nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
		return normalizeConversationItemsLimit([]json.RawMessage{item}, true, maxItemsPerConversation)
	}
	var items []json.RawMessage
	if json.Unmarshal(trimmed, &items) != nil {
		return nil, errors.New("input must be text or an input-item array")
	}
	return normalizeConversationItemsLimit(items, true, maxItemsPerConversation)
}

func responseWithConversation(body []byte, attachment *conversationAttachment) ([]byte, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return nil, errors.New("provider response is not a JSON object")
	}
	var object, status string
	if json.Unmarshal(value["object"], &object) != nil || object != "response" || json.Unmarshal(value["status"], &status) != nil || status == "" {
		return nil, errors.New("provider response is not a valid Response object")
	}
	var output []json.RawMessage
	if json.Unmarshal(value["output"], &output) != nil {
		return nil, errors.New("provider response output is invalid")
	}
	normalized, err := normalizeConversationItemsLimit(output, true, maxItemsPerConversation)
	if err != nil {
		return nil, err
	}
	attachment.outputItems = normalized
	value["output"], _ = json.Marshal(normalized)
	value["conversation"], _ = json.Marshal(map[string]string{"id": attachment.id})
	return json.Marshal(value)
}

func responseStreamWithConversation(body []byte, attachment *conversationAttachment) ([]byte, bool, error) {
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	terminalType := ""
	var normalizedResponse map[string]any
	var originalOutput, normalizedOutput []any
	err := walkConversationStreamFrames(body, func(frame []byte) error {
		data := conversationStreamFrameData(frame)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		var value map[string]any
		if json.Unmarshal(data, &value) != nil || value == nil {
			return errors.New("response stream contains invalid JSON")
		}
		eventType := stringValue(value["type"])
		if eventType != "response.completed" && eventType != "response.incomplete" && eventType != "response.failed" && eventType != "error" {
			return nil
		}
		if terminalType != "" {
			return errors.New("response stream contains multiple terminal events")
		}
		terminalType = eventType
		if eventType == "error" {
			return nil
		}
		response, ok := value["response"].(map[string]any)
		if !ok || response["object"] != "response" || response["status"] != eventType[len("response."):] {
			return errors.New("response stream terminal event is invalid")
		}
		if eventType == "response.failed" {
			return nil
		}
		originalOutput, _ = response["output"].([]any)
		encoded, _ := json.Marshal(response)
		normalized, err := responseWithConversation(encoded, attachment)
		if err != nil {
			return err
		}
		if json.Unmarshal(normalized, &normalizedResponse) != nil {
			return errors.New("response stream terminal event is invalid")
		}
		normalizedOutput, _ = normalizedResponse["output"].([]any)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if terminalType == "" {
		return nil, false, errors.New("response stream did not reach a terminal event")
	}
	terminalSuccess := terminalType == "response.completed" || terminalType == "response.incomplete"
	if terminalSuccess && len(originalOutput) != len(normalizedOutput) {
		return nil, false, errors.New("response stream output is invalid")
	}
	idsByIndex, idsByOld := make([]string, len(normalizedOutput)), map[string]string{}
	for index, item := range normalizedOutput {
		next := stringValue(objectMap(item)["id"])
		if next == "" {
			return nil, false, errors.New("response stream output is invalid")
		}
		idsByIndex[index] = next
		if old := stringValue(objectMap(originalOutput[index])["id"]); old != "" {
			idsByOld[old] = next
		}
	}
	conversation := map[string]any{"id": attachment.id}
	var rewritten bytes.Buffer
	err = walkConversationStreamFrames(body, func(frame []byte) error {
		data := conversationStreamFrameData(frame)
		if len(data) == 0 {
			rewritten.Write(frame)
			rewritten.WriteString("\n\n")
			if rewritten.Len() > maxInferenceBody {
				return errors.New("response stream exceeds 16 MiB")
			}
			return nil
		}
		var encoded []byte
		if bytes.Equal(data, []byte("[DONE]")) {
			encoded = data
		} else {
			var value map[string]any
			if json.Unmarshal(data, &value) != nil || value == nil {
				return errors.New("response stream contains invalid JSON")
			}
			eventType := stringValue(value["type"])
			if terminalSuccess && eventType == terminalType {
				value["response"] = normalizedResponse
			} else if response, ok := value["response"].(map[string]any); ok {
				response["conversation"] = conversation
				if terminalSuccess {
					rewriteConversationStreamOutput(response["output"], -1, idsByIndex, idsByOld)
				}
			}
			if terminalSuccess {
				outputIndex := -1
				if candidate, ok := integer(value["output_index"]); ok {
					outputIndex = int(candidate)
				}
				rewriteConversationStreamOutput(value["item"], outputIndex, idsByIndex, idsByOld)
				if old := stringValue(value["item_id"]); old != "" {
					if next := idsByOld[old]; next != "" {
						value["item_id"] = next
					} else if outputIndex >= 0 && outputIndex < len(idsByIndex) {
						value["item_id"] = idsByIndex[outputIndex]
					}
				}
			}
			encoded, _ = json.Marshal(value)
		}
		walkConversationStreamLines(frame, func(line []byte) {
			if !bytes.HasPrefix(line, []byte("data:")) {
				rewritten.Write(line)
				rewritten.WriteByte('\n')
			}
		})
		rewritten.WriteString("data: ")
		rewritten.Write(encoded)
		rewritten.WriteString("\n\n")
		if rewritten.Len() > maxInferenceBody {
			return errors.New("response stream exceeds 16 MiB")
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return rewritten.Bytes(), terminalSuccess, nil
}

func walkConversationStreamFrames(body []byte, visit func([]byte) error) error {
	count := 0
	for len(body) > 0 {
		end := bytes.Index(body, []byte("\n\n"))
		frame := body
		if end >= 0 {
			frame, body = body[:end], body[end+2:]
		} else {
			body = nil
		}
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		count++
		if count > maxConversationStreamFrames {
			return errors.New("response stream contains too many events")
		}
		if err := visit(frame); err != nil {
			return err
		}
	}
	return nil
}

func conversationStreamFrameData(frame []byte) []byte {
	var data bytes.Buffer
	walkConversationStreamLines(frame, func(line []byte) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			return
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
	})
	return data.Bytes()
}

func walkConversationStreamLines(frame []byte, visit func([]byte)) {
	for len(frame) > 0 {
		end := bytes.IndexByte(frame, '\n')
		line := frame
		if end >= 0 {
			line, frame = frame[:end], frame[end+1:]
		} else {
			frame = nil
		}
		visit(line)
	}
}

func rewriteConversationStreamOutput(value any, outputIndex int, idsByIndex []string, idsByOld map[string]string) {
	if output, ok := value.([]any); ok {
		for index, item := range output {
			rewriteConversationStreamOutput(item, index, idsByIndex, idsByOld)
		}
		return
	}
	item := objectMap(value)
	if item == nil {
		return
	}
	if next := idsByOld[stringValue(item["id"])]; next != "" {
		item["id"] = next
	} else if outputIndex >= 0 && outputIndex < len(idsByIndex) {
		item["id"] = idsByIndex[outputIndex]
	}
}

func appendConversationTurn(ctx context.Context, tx *sql.Tx, attachment conversationAttachment, now int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE conversations SET revision=revision+1 WHERE id=? AND key_id=? AND deleted_at IS NULL AND revision=?", attachment.id, attachment.keyID, attachment.revision)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errConversationChanged
	}
	items := append(append([]json.RawMessage{}, attachment.newItems...), attachment.outputItems...)
	if err := checkConversationCapacity(ctx, tx, attachment.ownerID, attachment.keyID, 0, conversationItemsSize(items)); err != nil {
		return err
	}
	return insertConversationItems(ctx, tx, attachment.id, items, now)
}

func (handler *Handler) storeConversationTurn(ctx context.Context, attachment conversationAttachment) error {
	tx, err := handler.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendConversationTurn(ctx, tx, attachment, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
