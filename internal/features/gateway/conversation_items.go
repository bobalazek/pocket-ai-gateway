package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	retainedConversations          = 10000
	retainedConversationBytes      = 1 << 30
	retainedOwnerConversations     = 2500
	retainedConversationOwnerBytes = 512 << 20
	retainedKeyConversations       = 250
	maxItemsPerConversation        = 10000
	maxConversationBytes           = 64 << 20
)

var (
	retainedConversationKeyBytes int64 = 128 << 20
	errConversationLimit               = errors.New("conversation retention limit reached")
	errConversationItemLimit           = errors.New("conversation item limit reached")
)

func conversationMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil || len(value) > 16 {
		return nil, errors.New("metadata must contain at most 16 string pairs")
	}
	for key, item := range value {
		text, ok := item.(string)
		if !ok || utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(text) > 512 {
			return nil, errors.New("metadata keys must be at most 64 characters and values must be strings of at most 512 characters")
		}
	}
	return value, nil
}

func normalizeConversationItems(raw []json.RawMessage, allowEmpty bool) ([]json.RawMessage, error) {
	return normalizeConversationItemsLimit(raw, allowEmpty, 20)
}

func normalizeConversationItemsLimit(raw []json.RawMessage, allowEmpty bool, limit int) ([]json.RawMessage, error) {
	if len(raw) == 0 && allowEmpty {
		return []json.RawMessage{}, nil
	}
	if len(raw) < 1 {
		return nil, errors.New("items must contain at least one object")
	}
	if len(raw) > limit {
		return nil, errors.New("too many conversation items")
	}
	items := make([]json.RawMessage, 0, len(raw))
	for _, source := range raw {
		var item map[string]any
		if json.Unmarshal(source, &item) != nil || item == nil {
			return nil, errors.New("each item must be an object with a type")
		}
		itemType, _ := item["type"].(string)
		if itemType == "" {
			if _, roleOK := item["role"].(string); !roleOK {
				return nil, errors.New("each item must be a supported input item")
			}
			itemType, item["type"] = "message", "message"
		}
		if err := rejectCompactReferences(item); err != nil {
			return nil, err
		}
		if err := validateConversationItem(itemType, item); err != nil {
			return nil, err
		}
		id, err := credentials.RandomToken(18)
		if err != nil {
			return nil, err
		}
		item["id"] = "citem_" + id
		if item["status"] == nil {
			item["status"] = "completed"
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		items = append(items, encoded)
	}
	return items, nil
}

func validateConversationItem(itemType string, item map[string]any) error {
	if status, exists := item["status"]; exists && status != nil {
		value, ok := status.(string)
		if !ok || (value != "in_progress" && value != "completed" && value != "incomplete") {
			return errors.New("item status must be in_progress, completed, or incomplete")
		}
	}
	switch itemType {
	case "message":
		role, roleOK := item["role"].(string)
		if !roleOK || (role != "user" && role != "assistant" && role != "system" && role != "developer") {
			return errors.New("message role must be user, assistant, system, or developer")
		}
		switch content := item["content"].(type) {
		case string:
			item["content"] = []any{map[string]any{"type": "input_text", "text": content}}
		case []any:
			if len(content) == 0 {
				return errors.New("message content must not be empty")
			}
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok || !validConversationContent(part) {
					return errors.New("message content contains an unsupported or malformed part")
				}
			}
		default:
			return errors.New("message content must be text or a non-empty content array")
		}
	case "function_call":
		if !hasStrings(item, "arguments", "call_id", "name") {
			return errors.New("function_call requires arguments, call_id, and name")
		}
	case "function_call_output":
		if _, ok := item["output"].(string); !ok || !hasStrings(item, "call_id") {
			return errors.New("function_call_output requires call_id and string output")
		}
	default:
		return errors.New("unsupported conversation item type: " + itemType)
	}
	return nil
}

func validConversationContent(part map[string]any) bool {
	switch part["type"] {
	case "input_text", "output_text":
		_, ok := part["text"].(string)
		if !ok || part["type"] != "output_text" {
			return ok
		}
		if part["annotations"] == nil {
			part["annotations"] = []any{}
			return true
		}
		_, ok = part["annotations"].([]any)
		return ok
	case "refusal":
		_, ok := part["refusal"].(string)
		return ok
	case "input_image":
		url, urlOK := part["image_url"].(string)
		detail, detailOK := part["detail"].(string)
		return urlOK && url != "" && detailOK && (detail == "auto" || detail == "low" || detail == "high" || detail == "original")
	default:
		return false
	}
}

func hasStrings(item map[string]any, fields ...string) bool {
	for _, field := range fields {
		if value, ok := item[field].(string); !ok || value == "" {
			return false
		}
	}
	return true
}

func insertConversationItems(ctx context.Context, tx *sql.Tx, conversationID string, items []json.RawMessage, now int64) error {
	var count, size, ordinal int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(body_json)),0),COALESCE(MAX(ordinal),0) FROM conversation_items WHERE conversation_id=?", conversationID).Scan(&count, &size, &ordinal); err != nil {
		return err
	}
	incoming := int64(0)
	for _, item := range items {
		incoming += int64(len(item))
	}
	if count+int64(len(items)) > maxItemsPerConversation || size+incoming > maxConversationBytes {
		return errConversationItemLimit
	}
	for _, item := range items {
		ordinal++
		var value map[string]json.RawMessage
		_ = json.Unmarshal(item, &value)
		var id string
		_ = json.Unmarshal(value["id"], &id)
		if _, err := tx.ExecContext(ctx, "INSERT INTO conversation_items(conversation_id,id,ordinal,body_json,created_at) VALUES(?,?,?,?,?)", conversationID, id, ordinal, item, now); err != nil {
			return err
		}
	}
	return nil
}

func conversationItemsSize(items []json.RawMessage) int64 {
	var size int64
	for _, item := range items {
		size += int64(len(item))
	}
	return size
}

func purgeDeletedConversations(ctx context.Context, tx *sql.Tx, before int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_items WHERE conversation_id IN (SELECT id FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at<?)", before); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at<?", before)
	return err
}

func checkConversationCapacity(ctx context.Context, query responseQueryer, ownerID, keyID string, incomingCount, incomingBytes int64) error {
	var count, size, ownerCount, ownerSize, keyCount, keySize int64
	err := query.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0)),0),
		COALESCE(SUM(owner_user_id=?),0),COALESCE(SUM(CASE WHEN owner_user_id=? THEN length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0) ELSE 0 END),0),
		COALESCE(SUM(key_id=?),0),COALESCE(SUM(CASE WHEN key_id=? THEN length(metadata_json)+COALESCE((SELECT SUM(length(body_json)) FROM conversation_items WHERE conversation_id=conversations.id),0) ELSE 0 END),0)
		FROM conversations`, ownerID, ownerID, keyID, keyID).Scan(&count, &size, &ownerCount, &ownerSize, &keyCount, &keySize)
	if err != nil {
		return err
	}
	if count+incomingCount > retainedConversations || size+incomingBytes > retainedConversationBytes || ownerCount+incomingCount > retainedOwnerConversations || ownerSize+incomingBytes > retainedConversationOwnerBytes || keyCount+incomingCount > retainedKeyConversations || keySize+incomingBytes > retainedConversationKeyBytes {
		return errConversationLimit
	}
	return nil
}

func readConversation(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id, keyID string) (conversation, error) {
	var value conversation
	var metadata []byte
	var created int64
	err := query.QueryRowContext(ctx, "SELECT id,metadata_json,created_at FROM conversations WHERE id=? AND key_id=? AND deleted_at IS NULL", id, keyID).Scan(&value.ID, &metadata, &created)
	if err != nil {
		return value, err
	}
	value.Object, value.CreatedAt = "conversation", created/1000
	if json.Unmarshal(metadata, &value.Metadata) != nil {
		return conversation{}, errors.New("invalid stored conversation metadata")
	}
	return value, nil
}

func readConversationItems(ctx context.Context, database *sql.DB, id, keyID, after, order string, limit int) ([]json.RawMessage, bool, error) {
	if _, err := readConversation(ctx, database, id, keyID); err != nil {
		return nil, false, err
	}
	var cursor int64
	if after != "" {
		if err := database.QueryRowContext(ctx, "SELECT ordinal FROM conversation_items WHERE conversation_id=? AND id=?", id, after).Scan(&cursor); err != nil {
			return nil, false, err
		}
	}
	comparison, direction := ">", "ASC"
	if order == "desc" {
		comparison, direction = "<", "DESC"
	}
	query := "SELECT body_json FROM conversation_items WHERE conversation_id=? AND (?='' OR ordinal " + comparison + " ?) ORDER BY ordinal " + direction + " LIMIT ?"
	rows, err := database.QueryContext(ctx, query, id, after, cursor, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, limit)
	more := false
	for rows.Next() {
		var item []byte
		if err := rows.Scan(&item); err != nil {
			return nil, false, err
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, json.RawMessage(item))
	}
	return items, more, rows.Err()
}

func conversationItemPage(items []json.RawMessage, more bool) map[string]any {
	result := map[string]any{"object": "list", "data": items, "has_more": more, "first_id": "", "last_id": ""}
	if len(items) > 0 {
		var first, last map[string]json.RawMessage
		_ = json.Unmarshal(items[0], &first)
		_ = json.Unmarshal(items[len(items)-1], &last)
		var firstID, lastID string
		_ = json.Unmarshal(first["id"], &firstID)
		_ = json.Unmarshal(last["id"], &lastID)
		result["first_id"], result["last_id"] = firstID, lastID
	}
	return result
}

func hasInclude(request *http.Request) bool {
	return len(request.URL.Query()["include"]) > 0 || len(request.URL.Query()["include[]"]) > 0
}

func onlyJSONFields(body []byte, allowed ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return false
	}
	for field := range fields {
		found := false
		for _, name := range allowed {
			found = found || field == name
		}
		if !found {
			return false
		}
	}
	return true
}
