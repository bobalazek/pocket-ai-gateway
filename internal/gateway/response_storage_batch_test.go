package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

func TestRetainedResponseCapacityIncludesActiveMessageBatchReservations(t *testing.T) {
	ctx, store, owner, _, _, _ := gatewayFixture(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_batch_capacity',?,'Batch','active','[]',?,?)`, owner.ID, now, now); err != nil {
		t.Fatal(err)
	}
	for batch := range 2 {
		batchID := fmt.Sprintf("msgbatch_capacity_%d", batch)
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,created_at,expires_at) VALUES(?,?,'key_batch_capacity',?,?)`, batchID, owner.ID, now, now+int64(time.Hour/time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		for ordinal := 1; ordinal <= 4; ordinal++ {
			if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES(?,?,?,'{}',?)`, batchID, ordinal, fmt.Sprintf("capacity_item_%d_%d", batch, ordinal), maxInferenceBody-2); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_batch_capacity", 0, 1); !errors.Is(err, errRetainedResourceLimit) {
		t.Fatalf("active batch reservation capacity error=%v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE message_batch_items SET state='succeeded',result_json='{}',finished_at=?`, now); err != nil {
		t.Fatal(err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_batch_capacity", 0, 1); err != nil {
		t.Fatalf("terminal batch retained capacity error=%v", err)
	}
}

func TestBackgroundQueueCapacityIsSharedWithMessageBatches(t *testing.T) {
	batchBody := `{"requests":[{"custom_id":"a","params":{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"a"}]}},{"custom_id":"b","params":{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"b"}]}},{"custom_id":"c","params":{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"c"}]}},{"custom_id":"d","params":{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"d"}]}}]}`
	for _, batchFirst := range []bool{true, false} {
		name := "responses_then_batch"
		if batchFirst {
			name = "batch_then_response"
		}
		t.Run(name, func(t *testing.T) {
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			connection, _ := publishModel(t, ctx, providerService, owner, "openai", "http://127.0.0.1:1/v1", "upstream", []string{"chat"})
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Shared queue", Scopes: []string{"messages:batches", "chat:generate", "responses:generate"}, ModelPatterns: []string{"assistant"}, ConnectionIDs: []string{connection.ID}})
			if err != nil {
				t.Fatal(err)
			}
			handler := New(store.SystemDB(), keyService, providerService, usageService)
			mux := http.NewServeMux()
			handler.Register(mux)

			if batchFirst {
				created := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, batchBody)
				if created.Code != http.StatusOK {
					t.Fatalf("create batch = %d %s", created.Code, created.Body.String())
				}
				blocked := performResponseRequest(t, mux, secret, http.MethodPost, "/api/openai/v1/responses", `{"model":"assistant","input":"blocked","background":true}`)
				if blocked.Code != http.StatusTooManyRequests {
					t.Fatalf("response after batch = %d %s", blocked.Code, blocked.Body.String())
				}
				return
			}

			for range backgroundKeyJobs {
				createBackground(t, mux, secret, `"queued"`)
			}
			blocked := performMessageBatchRequest(t, mux, http.MethodPost, "/api/anthropic/v1/messages/batches", secret, batchBody)
			if blocked.Code != http.StatusTooManyRequests {
				t.Fatalf("batch after responses = %d %s", blocked.Code, blocked.Body.String())
			}
		})
	}
}
