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

func TestSharedCapacityIncludesOpenAIBatchParentAndEncryptedItems(t *testing.T) {
	ctx, store, owner, _, _, _ := gatewayFixture(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_openai_capacity',?,'OpenAI Batch','active','[]',?,?)`, owner.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_capacity',?,'key_openai_capacity','file_input','/v1/responses','24h','model','{}',3600,4,?,?,?,?)`, owner.ID, now, now, now+int64(24*time.Hour/time.Millisecond), now+int64(30*24*time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for ordinal := 1; ordinal <= 4; ordinal++ {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_capacity',?,?,?,?,?,?,?)`, ordinal, fmt.Sprintf("item_%d", ordinal), fmt.Sprintf("batch_req_%016d", ordinal), 2, make([]byte, 18), make([]byte, 12), openAIBatchResultReservation(4)); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkBackgroundQueueCapacity(ctx, store.SystemDB(), owner.ID, "key_openai_capacity", 1, 1); !errors.Is(err, errBackgroundQueueLimit) {
		t.Fatalf("OpenAI batch queue capacity error=%v", err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_openai_capacity", retainedKeyJobs-5, 0); err != nil {
		t.Fatalf("OpenAI batch resources below count capacity error=%v", err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_openai_capacity", retainedKeyJobs-4, 0); !errors.Is(err, errRetainedResourceLimit) {
		t.Fatalf("OpenAI batch resource count error=%v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `DELETE FROM openai_batch_items WHERE batch_id='batch_capacity'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_batches SET status='completed',request_completed=4,terminal_at=? WHERE id='batch_capacity'`, now); err != nil {
		t.Fatal(err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_openai_capacity", retainedKeyJobs-1, 0); err != nil {
		t.Fatalf("terminal OpenAI batch parent at capacity error=%v", err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_openai_capacity", retainedKeyJobs, 0); !errors.Is(err, errRetainedResourceLimit) {
		t.Fatalf("terminal OpenAI batch parent count error=%v", err)
	}
}

func TestSharedCapacityIncludesPendingOpenAIUploads(t *testing.T) {
	ctx, store, owner, _, _, _ := gatewayFixture(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_upload_capacity',?,'OpenAI Upload','active','[]',?,?)`, owner.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_capacity',?,'key_upload_capacity','batch.jsonl','batch','application/jsonl',1,3600,?,?)`, owner.ID, now, now+int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_upload_capacity", retainedKeyJobs-1, 0); err != nil {
		t.Fatalf("pending upload below count capacity error=%v", err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_upload_capacity", retainedKeyJobs, 0); !errors.Is(err, errRetainedResourceLimit) {
		t.Fatalf("pending upload count capacity error=%v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE openai_uploads SET status='completed',completed_at=? WHERE id='upload_capacity'`, now); err != nil {
		t.Fatal(err)
	}
	if err := checkRetainedResourceCapacity(ctx, store.SystemDB(), owner.ID, "key_upload_capacity", retainedKeyJobs, 0); err != nil {
		t.Fatalf("completed upload reserved capacity error=%v", err)
	}
}
