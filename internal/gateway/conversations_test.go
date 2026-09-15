package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
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

func TestBackgroundResponseConversationAttachmentSurvivesQueueRestart(t *testing.T) {
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamBody, _ = io.ReadAll(request.Body)
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[{"id":"msg_upstream","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
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
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Second","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID           string `json:"id"`
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
	}
	if queued.Code != http.StatusOK || json.Unmarshal(queued.Body.Bytes(), &queuedResponse) != nil || queuedResponse.ID == "" || queuedResponse.Conversation.ID != conversation.ID {
		t.Fatalf("queued = %d %s", queued.Code, queued.Body.String())
	}
	var durableConversation string
	var durableRevision int64
	var durableItems []byte
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT conversation_id,conversation_revision,conversation_items_json FROM stored_responses WHERE id=?", queuedResponse.ID).Scan(&durableConversation, &durableRevision, &durableItems); err != nil || durableConversation != conversation.ID || durableRevision == 0 || len(durableItems) == 0 {
		t.Fatalf("durable attachment = %q %d %s, %v", durableConversation, durableRevision, durableItems, err)
	}

	worker := New(store.SystemDB(), keyService, providerService, usageService)
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "completed")
	retrieved := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+queuedResponse.ID, "")
	var completed struct {
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
		Output []struct {
			ID string `json:"id"`
		} `json:"output"`
	}
	if retrieved.Code != http.StatusOK || json.Unmarshal(retrieved.Body.Bytes(), &completed) != nil || completed.Conversation.ID != conversation.ID || len(completed.Output) != 1 || !strings.HasPrefix(completed.Output[0].ID, "citem_") {
		t.Fatalf("completed = %d %s", retrieved.Code, retrieved.Body.String())
	}
	inputs := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+queuedResponse.ID+"/input_items?order=asc", "")
	listed := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items?order=asc&limit=100", "")
	var inputPage, conversationPage struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(inputs.Body.Bytes(), &inputPage) != nil || json.Unmarshal(listed.Body.Bytes(), &conversationPage) != nil || len(inputPage.Data) != 2 || len(conversationPage.Data) != 3 || inputPage.Data[0].ID != conversationPage.Data[0].ID || inputPage.Data[1].ID != conversationPage.Data[1].ID || completed.Output[0].ID != conversationPage.Data[2].ID {
		t.Fatalf("inputs=%s conversation=%s", inputs.Body.String(), listed.Body.String())
	}
	if !bytes.Contains(upstreamBody, []byte("First")) || !bytes.Contains(upstreamBody, []byte("Second")) || bytes.Contains(upstreamBody, []byte("citem_")) || bytes.Contains(upstreamBody, []byte(conversation.ID)) || bytes.Contains(upstreamBody, []byte(`"background":true`)) {
		t.Fatalf("upstream body = %s", upstreamBody)
	}
	var requestState string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state FROM requests").Scan(&requestState); err != nil || requestState != "succeeded" {
		t.Fatalf("request state = %q, %v", requestState, err)
	}
}

func TestBackgroundResponseConversationAttachmentRejectsStaleHistory(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Stale","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &queuedResponse)
	mutated := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+conversation.ID+"/items", `{"items":[{"role":"user","content":"Newer"}]}`)
	if queued.Code != http.StatusOK || mutated.Code != http.StatusOK {
		t.Fatalf("queued=%d mutated=%d", queued.Code, mutated.Code)
	}
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "failed")
	failed := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/responses/"+queuedResponse.ID, "")
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"code":"conversation_conflict"`) || !strings.Contains(failed.Body.String(), `"conversation":{"id":"`+conversation.ID+`"}`) || upstreamCalls != 0 {
		t.Fatalf("failed = %d %s calls=%d", failed.Code, failed.Body.String(), upstreamCalls)
	}
	var items int
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?", conversation.ID).Scan(&items)
	if items != 1 {
		t.Fatalf("stale background stored %d items", items)
	}
}

func TestBackgroundConversationConflictKeepsProviderUsageAndFailsRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(response, `{"id":"resp_upstream","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Race","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &queuedResponse)
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("background request did not start")
	}
	mutated := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations/"+conversation.ID+"/items", `{"items":[{"role":"user","content":"Concurrent"}]}`)
	close(release)
	if queued.Code != http.StatusOK || mutated.Code != http.StatusOK {
		t.Fatalf("queued=%d mutated=%d", queued.Code, mutated.Code)
	}
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "failed")
	var requestState, attemptState string
	var inputTokens, outputTokens int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT requests.state,attempts.state,attempts.input_tokens,attempts.output_tokens FROM requests JOIN attempts ON attempts.request_id=requests.id`).Scan(&requestState, &attemptState, &inputTokens, &outputTokens); err != nil {
		t.Fatal(err)
	}
	if requestState != "failed" || attemptState != "succeeded" || inputTokens != 2 || outputTokens != 1 {
		t.Fatalf("accounting request=%s attempt=%s input=%d output=%d", requestState, attemptState, inputTokens, outputTokens)
	}
}

func TestBackgroundConversationRejectsInvalidProviderOutputBeforeAccounting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"object":"response","status":"completed","output":"invalid","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	queued := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Invalid","background":true,"conversation":"`+conversation.ID+`"}`)
	var queuedResponse struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &queuedResponse)
	workerContext, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunBackground(workerContext) }()
	defer func() { stop(); <-done }()
	waitResponseState(t, store.SystemDB(), queuedResponse.ID, "failed")
	var requestState, attemptState string
	var successes, failures int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT requests.state,attempts.state FROM requests JOIN attempts ON attempts.request_id=requests.id`).Scan(&requestState, &attemptState); err != nil {
		t.Fatal(err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT success_count,failure_count FROM route_observations`).Scan(&successes, &failures); err != nil {
		t.Fatal(err)
	}
	if queued.Code != http.StatusOK || requestState != "failed" || attemptState != "failed" || successes != 0 || failures != 1 {
		t.Fatalf("queued=%d request=%s attempt=%s route=%d/%d", queued.Code, requestState, attemptState, successes, failures)
	}
}

func TestBackgroundConversationCompletionIsAtomic(t *testing.T) {
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
	principal, _ := keyService.Authenticate(ctx, secret)
	envelope := map[string]json.RawMessage{"model": json.RawMessage(`"assistant"`), "input": json.RawMessage(`"Atomic"`), "background": json.RawMessage(`true`)}
	envelope["conversation"], _ = json.Marshal(conversation.ID)
	attachment, _, err := handler.prepareConversationResponse(ctx, principal, envelope)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := json.Marshal(attachment.newItems)
	now := time.Now()
	_, err = store.SystemDB().ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at,state,request_json,conversation_id,conversation_revision,conversation_items_json,lease_epoch) VALUES('resp_atomic',?,?,?,?,?,?,'running',?,?,?,?,?)`, owner.ID, principal.KeyID, "assistant", responseState("resp_atomic", "assistant", "in_progress", now, nil), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), attachment.requestBody, attachment.id, attachment.revision, items, handler.epoch)
	if err != nil {
		t.Fatal(err)
	}
	job := backgroundJob{id: "resp_atomic", ownerID: owner.ID, keyID: principal.KeyID, modelID: "assistant", createdAt: now, attachment: &attachment}
	result, err := responseWithConversation([]byte(`{"object":"response","status":"completed","output":[]}`), job.attachment)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := rewriteResponse(job.id, job.modelID, true, job.createdAt, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_background_completion BEFORE UPDATE OF state ON stored_responses WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := handler.completeBackgroundConversation(ctx, job, stored, "missing_request"); err == nil {
		t.Fatal("completion unexpectedly succeeded")
	}
	var state string
	var count int
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT state FROM stored_responses WHERE id=?", job.id).Scan(&state)
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?", conversation.ID).Scan(&count)
	if state != "running" || count != 0 {
		t.Fatalf("partial completion state=%s items=%d", state, count)
	}
}

func TestResponseConversationStreamRewritesItemIdentity(t *testing.T) {
	attachment := conversationAttachment{id: "conv_test", keyID: "key_test", ownerID: "usr_test"}
	stream := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_upstream\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_upstream\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_upstream\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_upstream\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_upstream\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_upstream\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	rewritten, terminalSuccess, err := responseStreamWithConversation(stream, &attachment)
	if err != nil || !terminalSuccess || len(attachment.outputItems) != 1 {
		t.Fatalf("rewrite error=%v output=%s", err, rewritten)
	}
	var item map[string]any
	_ = json.Unmarshal(attachment.outputItems[0], &item)
	id := stringValue(item["id"])
	if !strings.HasPrefix(id, "citem_") || bytes.Contains(rewritten, []byte("msg_upstream")) || strings.Count(string(rewritten), id) != 4 || strings.Count(string(rewritten), `"conversation":{"id":"conv_test"}`) != 2 {
		t.Fatalf("id=%q stream=%s", id, rewritten)
	}
	if _, _, err := responseStreamWithConversation([]byte("event: response.created\ndata: {}\n\n"), &attachment); err == nil {
		t.Fatal("incomplete stream unexpectedly accepted")
	}
	failed := []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
	failedAttachment := conversationAttachment{id: "conv_test"}
	rewritten, terminalSuccess, err = responseStreamWithConversation(failed, &failedAttachment)
	if err != nil || terminalSuccess || !bytes.Contains(rewritten, []byte(`"type":"response.failed"`)) || !bytes.Contains(rewritten, []byte(`"conversation":{"id":"conv_test"}`)) || len(failedAttachment.outputItems) != 0 {
		t.Fatalf("failed rewrite success=%v err=%v output=%s", terminalSuccess, err, rewritten)
	}
	errorEvent := []byte("event: error\ndata: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"failed\"}\n\n")
	rewritten, terminalSuccess, err = responseStreamWithConversation(errorEvent, &attachment)
	if err != nil || terminalSuccess || !bytes.Contains(rewritten, []byte(`"type":"error"`)) {
		t.Fatalf("error rewrite success=%v err=%v output=%s", terminalSuccess, err, rewritten)
	}
	if _, _, err := responseStreamWithConversation(bytes.Repeat([]byte(": keepalive\n\n"), maxConversationStreamFrames+1), &attachment); err == nil || !strings.Contains(err.Error(), "too many events") {
		t.Fatalf("frame limit error = %v", err)
	}
	manyLines := append(bytes.Repeat([]byte(":x\n"), 1_000_000), []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")...)
	if _, terminalSuccess, err := responseStreamWithConversation(manyLines, &conversationAttachment{id: "conv_lines"}); err != nil || !terminalSuccess {
		t.Fatalf("many-line frame success=%v error=%v", terminalSuccess, err)
	}
	boundary := append(append([]byte{}, stream...), []byte(": "+strings.Repeat("x", maxInferenceBody-len(stream)-4)+"\n\n")...)
	if len(boundary) != maxInferenceBody {
		t.Fatalf("boundary size = %d", len(boundary))
	}
	if _, _, err := responseStreamWithConversation(boundary, &conversationAttachment{id: "conv_boundary"}); err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Fatalf("rewritten boundary error = %v", err)
	}
}

func TestStreamingResponseConversationAttachment(t *testing.T) {
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamBody, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "anthropic", upstream.URL+"/v1", "anthropic-upstream", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.SystemDB(), keyService, providerService, usageService)
	mux := http.NewServeMux()
	handler.Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{"items":[{"role":"user","content":"First"}]}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	streamed := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Second","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
	if streamed.Code != http.StatusOK || !strings.Contains(streamed.Body.String(), `"type":"response.output_text.delta"`) || !strings.Contains(streamed.Body.String(), `"conversation":{"id":"`+conversation.ID+`"}`) || strings.Contains(streamed.Body.String(), "msg_translated") {
		t.Fatalf("stream = %d %s", streamed.Code, streamed.Body.String())
	}
	if !bytes.Contains(upstreamBody, []byte("First")) || !bytes.Contains(upstreamBody, []byte("Second")) || bytes.Contains(upstreamBody, []byte("citem_")) {
		t.Fatalf("upstream body = %s", upstreamBody)
	}
	listed := performResponseRequest(t, mux, secret, http.MethodGet, "/api/openai/v1/conversations/"+conversation.ID+"/items?order=asc&limit=100", "")
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Data) != 3 || !strings.Contains(streamed.Body.String(), stringValue(page.Data[2]["id"])) {
		t.Fatalf("items = %d %s", listed.Code, listed.Body.String())
	}
	rejected := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Stored","stream":true,"conversation":"`+conversation.ID+`"}`)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "stored streaming Responses are not supported") {
		t.Fatalf("stored stream = %d %s", rejected.Code, rejected.Body.String())
	}
	if _, err := store.SystemDB().ExecContext(ctx, `CREATE TRIGGER block_stream_turn BEFORE INSERT ON conversation_items BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	failed := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Atomic","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
	if failed.Code != http.StatusServiceUnavailable || strings.Contains(failed.Body.String(), "event: response.") {
		t.Fatalf("failed stream = %d %s", failed.Code, failed.Body.String())
	}
}

func TestNativeResponseConversationStreamOutcomes(t *testing.T) {
	tests := []struct {
		name, stream, state, usageStatus string
		status, items, input, output     int
		contains                         string
	}{
		{
			name:        "usage beyond accounting capture",
			stream:      ": " + strings.Repeat("x", (8<<20)+1024) + "\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_large\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n",
			state:       "succeeded",
			usageStatus: "provider_reported",
			status:      http.StatusOK,
			items:       1,
			input:       7,
			output:      3,
			contains:    "response.completed",
		},
		{
			name:        "typed provider failure",
			stream:      "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n",
			state:       "failed",
			usageStatus: "provider_reported",
			status:      http.StatusOK,
			input:       2,
			output:      1,
			contains:    "response.failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(response, test.stream)
			}))
			defer upstream.Close()
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
			created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
			var conversation conversation
			_ = json.Unmarshal(created.Body.Bytes(), &conversation)
			result := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Run","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
			if result.Code != test.status || !strings.Contains(result.Body.String(), test.contains) {
				t.Fatalf("response = %d %s", result.Code, result.Body.String())
			}
			var state, usageStatus string
			var input, output int
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,input_tokens,output_tokens FROM attempts").Scan(&state, &usageStatus, &input, &output); err != nil || state != test.state || usageStatus != test.usageStatus || input != test.input || output != test.output {
				t.Fatalf("attempt = %s/%s %d/%d, %v", state, usageStatus, input, output, err)
			}
			var items int
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_items WHERE conversation_id=?", conversation.ID).Scan(&items); err != nil || items != test.items {
				t.Fatalf("items = %d, %v", items, err)
			}
		})
	}

	t.Run("buffer overflow", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, strings.Repeat("x", maxInferenceBody+1))
		}))
		defer upstream.Close()
		ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
		defer store.Close()
		connection, model := publishModel(t, ctx, providerService, owner, "openai", upstream.URL+"/v1", "openai-upstream", []string{"chat"})
		_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
		created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
		var conversation conversation
		_ = json.Unmarshal(created.Body.Bytes(), &conversation)
		result := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Run","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
		if result.Code != http.StatusBadGateway || strings.Contains(result.Body.String(), "event:") {
			t.Fatalf("response = %d %s", result.Code, result.Body.String())
		}
		var input, output sql.NullInt64
		if err := store.SystemDB().QueryRowContext(ctx, "SELECT input_tokens,output_tokens FROM attempts").Scan(&input, &output); err != nil || input.Valid || output.Valid {
			t.Fatalf("usage = %v/%v, %v", input, output, err)
		}
	})
}

func TestAttachedTranslatedStreamDoesNotFallbackAfterSemanticFailure(t *testing.T) {
	firstCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		firstCalls++
		response.Header().Set("Content-Type", "text/event-stream")
		if firstCalls == 1 {
			_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"padding\":\""+strings.Repeat("x", (8<<20)+1024)+"\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		_, _ = io.WriteString(response, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Billed\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer first.Close()
	fallbackCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer second.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	firstConnection, model := publishModel(t, ctx, providerService, owner, "anthropic", first.URL+"/v1", "first", []string{"chat"})
	secondConnection, err := providerService.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "fallback", Adapter: "anthropic", BaseURL: second.URL + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := providerService.PutCredential(ctx, owner, secondConnection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	secondModel, err := providerService.CreateUpstreamModel(ctx, owner, secondConnection.ID, "second", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerService.ConfigureRoute(ctx, owner, model.ID, model.Revision, providers.RouteConfigInput{Strategy: "ordered_fallback", Targets: []providers.RouteTargetInput{{UpstreamModelID: model.TargetModelID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: secondModel.ID, Priority: 2, Weight: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Conversation", Scopes: []string{"responses:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{firstConnection.ID, secondConnection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	created := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/conversations", `{}`)
	var conversation conversation
	_ = json.Unmarshal(created.Body.Bytes(), &conversation)
	large := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Large line","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
	if large.Code != http.StatusOK || !bytes.Contains(large.Body.Bytes(), []byte("response.completed")) || fallbackCalls != 0 {
		t.Fatalf("large-line response=%d fallback calls=%d", large.Code, fallbackCalls)
	}
	result := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"`+model.ID+`","input":"Run","stream":true,"store":false,"conversation":"`+conversation.ID+`"}`)
	if result.Code != http.StatusBadGateway || fallbackCalls != 0 || strings.Contains(result.Body.String(), "Billed") {
		t.Fatalf("response=%d %s fallback calls=%d", result.Code, result.Body.String(), fallbackCalls)
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
