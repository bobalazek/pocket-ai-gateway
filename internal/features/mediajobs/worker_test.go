package mediajobs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func TestTerminalOutputFailurePreservesProviderCost(t *testing.T) {
	for _, test := range []struct {
		name             string
		output           json.RawMessage
		invalidMasterKey bool
		errorCode        string
	}{
		{
			name:      "oversized output",
			output:    json.RawMessage(`"` + strings.Repeat("x", maxOutputBytes) + `"`),
			errorCode: "provider_output_too_large",
		},
		{
			name:             "output encryption failure",
			output:           json.RawMessage(`{"url":"https://example.test/result.png"}`),
			invalidMasterKey: true,
			errorCode:        "storage_unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, store, owner, _, keyService, service, secret := mediaFixture(t, "replicate", "acme/image", "image")
			defer store.Close()
			principal := mustPrincipal(t, keyService, secret)
			job, err := service.Create(ctx, principal, CreateInput{Model: "media-model", MediaType: "image", Input: json.RawMessage(`{"prompt":"private"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.usage.MarkDispatching(ctx, job.attemptID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SystemDB().ExecContext(ctx, "UPDATE media_jobs SET state='processing' WHERE id=?", job.ID); err != nil {
				t.Fatal(err)
			}
			if test.invalidMasterKey {
				service.masterKey = bytes.Repeat([]byte{8}, 31)
			}

			cost := int64(25_000_000)
			prediction := providerPrediction{ID: "pred_costly", Status: "succeeded", Output: test.output, CostNanos: &cost}
			service.applyPrediction(ctx, job.ID, prediction)
			service.applyPrediction(ctx, job.ID, prediction)

			result, err := service.Get(ctx, principal, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(result.Error, &failure); err != nil {
				t.Fatal(err)
			}
			if result.State != "failed" || failure.Code != test.errorCode || len(result.Output) != 0 {
				t.Fatalf("job state=%q error=%q output=%s", result.State, failure.Code, result.Output)
			}
			var outputCiphertext, outputNonce, inputCiphertext []byte
			var outputBytes int64
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT output_ciphertext,output_nonce,output_bytes,input_ciphertext FROM media_jobs WHERE id=?", job.ID).Scan(&outputCiphertext, &outputNonce, &outputBytes, &inputCiphertext); err != nil {
				t.Fatal(err)
			}
			if len(outputCiphertext) != 0 || len(outputNonce) != 0 || outputBytes != 0 || len(inputCiphertext) != 0 {
				t.Fatal("terminal failure retained media content")
			}
			var state, usageStatus string
			var recordedCost int64
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,as_recorded_cost_nanos FROM attempts WHERE id=?", job.attemptID).Scan(&state, &usageStatus, &recordedCost); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || usageStatus != "provider_reported" || recordedCost != cost {
				t.Fatalf("attempt state=%q usage=%q cost=%d", state, usageStatus, recordedCost)
			}
			var settlements int
			if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_ledger WHERE attempt_id=? AND entry_type='settlement' AND cost_nanos=?", job.attemptID, cost).Scan(&settlements); err != nil || settlements != 1 {
				t.Fatalf("cost settlements=%d: %v", settlements, err)
			}
			if _, err := usage.ProjectOutbox(ctx, store, 100); err != nil {
				t.Fatal(err)
			}
			var projectedRequests, projectedCost, projectedUnknown int64
			if err := store.DataDB().QueryRowContext(ctx, "SELECT requests,known_cost_nanos,unknown_attempts FROM usage_daily WHERE owner_user_id=?", owner.ID).Scan(&projectedRequests, &projectedCost, &projectedUnknown); err != nil || projectedRequests != 1 || projectedCost != cost || projectedUnknown != 0 {
				t.Fatalf("projection requests=%d cost=%d unknown=%d: %v", projectedRequests, projectedCost, projectedUnknown, err)
			}
		})
	}
}
