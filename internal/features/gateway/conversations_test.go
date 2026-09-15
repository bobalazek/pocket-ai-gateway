package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestConversationLifecycleAndKeyIsolation(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	_, sibling, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Sibling", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)

	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{"metadata":{"topic":"demo"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil || !strings.HasPrefix(conversation.ID, "conv_") {
		t.Fatalf("conversation = %s, %v", created.Body.String(), err)
	}

	added := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+conversation.ID+"/items", `{"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Next"}]},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if added.Code != http.StatusOK {
		t.Fatalf("add = %d %s", added.Code, added.Body.String())
	}
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(added.Body.Bytes(), &page); err != nil || len(page.Data) != 2 {
		t.Fatalf("added page = %s, %v", added.Body.String(), err)
	}

	first := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items?order=asc&limit=1", "")
	var firstPage struct {
		Data    []map[string]any `json:"data"`
		LastID  string           `json:"last_id"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil || first.Code != http.StatusOK || len(firstPage.Data) != 1 || !firstPage.HasMore || firstPage.LastID == "" {
		t.Fatalf("first page = %d %s, %v", first.Code, first.Body.String(), err)
	}
	next := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items?order=asc&after="+firstPage.LastID, "")
	var nextPage struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &nextPage); err != nil || next.Code != http.StatusOK || len(nextPage.Data) != 2 {
		t.Fatalf("next page = %d %s, %v", next.Code, next.Body.String(), err)
	}

	item := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items/"+page.Data[0].ID, "")
	if item.Code != http.StatusOK || !strings.Contains(item.Body.String(), `"status":"completed"`) {
		t.Fatalf("item = %d %s", item.Code, item.Body.String())
	}
	isolated := performResponseRequest(t, mux, sibling, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID, "")
	if isolated.Code != http.StatusNotFound {
		t.Fatalf("sibling read = %d %s", isolated.Code, isolated.Body.String())
	}

	updated := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+conversation.ID, `{"metadata":{"topic":"updated"}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"topic":"updated"`) {
		t.Fatalf("update = %d %s", updated.Code, updated.Body.String())
	}
	deletedItem := performResponseRequest(t, mux, secret, http.MethodDelete, "/api/openai/v1/conversations/"+conversation.ID+"/items/"+page.Data[0].ID, "")
	if deletedItem.Code != http.StatusOK || !strings.Contains(deletedItem.Body.String(), `"object":"conversation"`) {
		t.Fatalf("delete item = %d %s", deletedItem.Code, deletedItem.Body.String())
	}
	deleted := performResponseRequest(t, mux, secret, http.MethodDelete, "/api/openai/v1/conversations/"+conversation.ID, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"object":"conversation.deleted"`) {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	missing := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted read = %d %s", missing.Code, missing.Body.String())
	}
}

func TestResponseConversationAttachmentAcrossProviders(t *testing.T) {
	responses := map[string]string{
		"openai":    `{"id":"resp_upstream","object":"response","status":"completed","output":[{"id":"msg_upstream","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`,
		"anthropic": `{"id":"msg_upstream","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		"gemini":    `{"responseId":"gem_upstream","candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`,
	}
	for target, upstreamResponse := range responses {
		t.Run(target, func(t *testing.T) {
			var upstreamBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				upstreamBody, _ = io.ReadAll(request.Body)
				_, _ = io.WriteString(response, upstreamResponse)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, target, upstream.URL+"/v1", target+"-upstream", []string{"chat"})
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			handler := New(store.SystemDB(), keyService, providerService, usageService)
			mux := http.NewServeMux()
			handler.Register(mux)
			created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{"items":[{"role":"user","content":"First"}]}`)
			var conversation conversation
			if json.Unmarshal(created.Body.Bytes(), &conversation) != nil {
				t.Fatalf("conversation = %s", created.Body.String())
			}
			attached := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Second","conversation":{"id":"`+conversation.ID+`"}}`)
			if attached.Code != http.StatusOK || !bytes.Contains(attached.Body.Bytes(), []byte(`"conversation":{"id":"`+conversation.ID+`"}`)) {
				t.Fatalf("attached = %d %s", attached.Code, attached.Body.String())
			}
			var attachedResponse struct {
				ID     string `json:"id"`
				Output []struct {
					ID string `json:"id"`
				} `json:"output"`
			}
			if json.Unmarshal(attached.Body.Bytes(), &attachedResponse) != nil || len(attachedResponse.Output) != 1 {
				t.Fatalf("attached response = %s", attached.Body.String())
			}
			inputs := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+attachedResponse.ID+"/input_items?order=asc", "")
			var inputPage struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if json.Unmarshal(inputs.Body.Bytes(), &inputPage) != nil || len(inputPage.Data) != 2 {
				t.Fatalf("response inputs = %d %s", inputs.Code, inputs.Body.String())
			}
			if !bytes.Contains(upstreamBody, []byte("First")) || !bytes.Contains(upstreamBody, []byte("Second")) || bytes.Contains(upstreamBody, []byte(conversation.ID)) || bytes.Contains(upstreamBody, []byte("citem_")) {
				t.Fatalf("upstream body = %s", upstreamBody)
			}
			continued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Third","store":false,"conversation":"`+conversation.ID+`"}`)
			if continued.Code != http.StatusOK || !bytes.Contains(upstreamBody, []byte("Hello")) || !bytes.Contains(upstreamBody, []byte("Third")) {
				t.Fatalf("continued = %d %s upstream=%s", continued.Code, continued.Body.String(), upstreamBody)
			}
			listed := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items?order=asc&limit=100", "")
			var page struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Data) != 5 || page.Data[0].ID != inputPage.Data[0].ID || page.Data[1].ID != inputPage.Data[1].ID || page.Data[2].ID != attachedResponse.Output[0].ID {
				t.Fatalf("items = %d %s", listed.Code, listed.Body.String())
			}
		})
	}
}

func TestResponseConversationAttachmentRejectsStaleHistory(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	principal, err := keyService.Authenticate(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]json.RawMessage{
		"model":        json.RawMessage(`"assistant"`),
		"input":        json.RawMessage(`"stale"`),
		"conversation": json.RawMessage(`"` + conversation.ID + `"`),
	}
	attachment, _, err := handler.prepareConversationResponse(ctx, principal, envelope)
	if err != nil {
		t.Fatal(err)
	}
	mutated := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+conversation.ID+"/items", `{"items":[{"role":"user","content":"newer"}]}`)
	if mutated.Code != http.StatusOK {
		t.Fatalf("mutation = %d %s", mutated.Code, mutated.Body.String())
	}
	if _, err := responseWithConversation([]byte(`{"object":"response","status":"completed","output":[]}`), &attachment); err != nil {
		t.Fatal(err)
	}
	if err := handler.storeConversationTurn(ctx, attachment); !errors.Is(err, errConversationChanged) {
		t.Fatalf("stale attachment error = %v", err)
	}
	var items int
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?", conversation.ID).Scan(&items)
	if items != 1 {
		t.Fatalf("stale attachment stored %d items", items)
	}
}

func TestResponseConversationAttachmentIsAtomicAndKeyOwned(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[{"id":"msg_upstream","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, sibling, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Sibling", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	denied := performResponseRequest(t, mux, sibling, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"No","store":false,"conversation":"`+conversation.ID+`"}`)
	if denied.Code != http.StatusNotFound || upstreamCalls != 0 {
		t.Fatalf("cross-key attachment = %d calls=%d", denied.Code, upstreamCalls)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_conversation_turn BEFORE INSERT ON conversation_items BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	failed := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Atomic","conversation":"`+conversation.ID+`"}`)
	if failed.Code != http.StatusServiceUnavailable || upstreamCalls != 1 {
		t.Fatalf("failed attachment = %d %s calls=%d", failed.Code, failed.Body.String(), upstreamCalls)
	}
	var responses, items int
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM stored_responses").Scan(&responses)
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?", conversation.ID).Scan(&items)
	if responses != 0 || items != 0 {
		t.Fatalf("partial storage responses=%d items=%d", responses, items)
	}
}

func TestConversationValidation(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	_, denied, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "No conversation scope", Scopes: []string{"models:read"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	for _, body := range []string{
		`{"metadata":{"bad":1}}`,
		`{"unknown":true}`,
		`{"items":[null]}`,
		`{"items":[{"type":"bogus"}]}`,
		`{"items":[{"type":"message","role":"user"}]}`,
		`{"items":[{"type":"function_call","arguments":"{}","call_id":"call_1"}]}`,
		`{"items":[{"type":"function_call_output","output":"done"}]}`,
		`{"items":[{"type":"item_reference","id":"item_provider"}]}`,
		`{"items":[{"type":"input_file","file_id":"file_provider"}]}`,
	} {
		result := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", body)
		if result.Code != http.StatusBadRequest {
			t.Fatalf("invalid create = %d %s", result.Code, result.Body.String())
		}
	}
	for _, body := range []string{"", "null", `{}`, `{"metadata":{"` + strings.Repeat("é", 64) + `":"ok"},"items":[{"role":"user","content":"Hello"}]}`} {
		empty := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", body)
		if empty.Code != http.StatusOK {
			t.Fatalf("valid conversation = %d %s", empty.Code, empty.Body.String())
		}
	}
	forbidden := performResponseRequest(t, mux, denied, http.MethodGet, "/api/openai/v1/conversations/missing", "")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden = %d %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestResponseConversationParameterValidation(t *testing.T) {
	for _, raw := range []string{`1`, `{}`, `{"id":"conv_1","extra":true}`, `{"id":null}`} {
		if _, err := responseConversationID(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted conversation parameter %s", raw)
		}
	}
	for _, raw := range []string{`"conv_1"`, `{"id":"conv_1"}`, `null`} {
		if _, err := responseConversationID(json.RawMessage(raw)); err != nil {
			t.Fatalf("rejected conversation parameter %s: %v", raw, err)
		}
	}
}

func TestConversationRetentionBoundsAndPurge(t *testing.T) {
	ctx, store, owner, keyService, _, _ := gatewayFixture(t)
	defer store.Close()
	key, _, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(checkConversationCapacity(ctx, store.SystemDB(), owner.ID, key.ID, 0, retainedConversationKeyBytes+1), errConversationLimit) {
		t.Fatal("key byte limit was not enforced")
	}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?) INSERT INTO conversations(id,owner_user_id,key_id,metadata_json,created_at,deleted_at) SELECT 'conv_cap_'||n,?,?, '{}',?,? FROM seq`, retainedKeyConversations, owner.ID, key.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(checkConversationCapacity(ctx, store.SystemDB(), owner.ID, key.ID, 1, 0), errConversationLimit) {
		t.Fatal("deleted conversations did not count toward the key limit")
	}
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO conversation_items(conversation_id,id,ordinal,body_json,created_at) VALUES('conv_cap_1','citem_old',1,'{}',?)", now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := purgeDeletedConversations(ctx, tx, now+1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var conversations, items int
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE key_id=?", key.ID).Scan(&conversations)
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE id='citem_old'").Scan(&items)
	if conversations != 0 || items != 0 {
		t.Fatalf("purge left conversations=%d items=%d", conversations, items)
	}
}

func TestConversationMetadataUpdateHonorsCapacity(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var value conversation
	if json.Unmarshal(created.Body.Bytes(), &value) != nil {
		t.Fatal("could not decode conversation")
	}
	previous := retainedConversationKeyBytes
	retainedConversationKeyBytes = 3
	t.Cleanup(func() { retainedConversationKeyBytes = previous })
	updated := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+value.ID, `{"metadata":{"a":"b"}}`)
	if updated.Code != http.StatusTooManyRequests {
		t.Fatalf("metadata growth = %d %s", updated.Code, updated.Body.String())
	}
}
