package usage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestPromptCacheSettlementIsUnpricedAtomicAndIdempotent(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 1000})
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "messages", Scope: "chat:generate", Dialect: "anthropic", TargetDialect: "anthropic", EstimatedInputTokens: 100, EstimatedOutputTokens: 10, OutputBounded: true})
	if err != nil {
		t.Fatal(err)
	}
	var price sql.NullString
	var estimate sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT price_version_id,estimated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&price, &estimate); err != nil || !price.Valid || !estimate.Valid || estimate.Int64 != 110_000 {
		t.Fatalf("price=%v estimate=%v err=%v", price, estimate, err)
	}
	input, output, creation, read, five, one := int64(60), int64(4), int64(20), int64(30), int64(15), int64(5)
	settlement := SettlementInput{IdempotencyKey: "cache-settlement", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, CacheCreationInputTokens: &creation, CacheReadInputTokens: &read, CacheCreation5mTokens: &five, CacheCreation1hTokens: &one, FinalRequest: true}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	var recorded, restated sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT as_recorded_cost_nanos,restated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&recorded, &restated); err != nil || recorded.Valid || restated.Valid {
		t.Fatalf("recorded=%v restated=%v err=%v", recorded, restated, err)
	}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	changedRead := int64(31)
	settlement.CacheReadInputTokens = &changedRead
	if err := service.Settle(ctx, admission.AttemptID, settlement); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed idempotent settlement=%v", err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 64 || reserved != 0 {
		t.Fatalf("tokens=%d/%d err=%v", consumed, reserved, err)
	}
	reprice := RepriceInput{ConnectionID: "conn_test", ModelID: "model_test", From: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), To: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), IdempotencyKey: "cache-reprice"}
	if preview, err := service.PreviewReprice(ctx, owner, reprice); err != nil || preview.AffectedAttempts != 0 || preview.MissingPrices != 0 || preview.DeltaUSD != "0" {
		t.Fatalf("reprice preview=%#v err=%v", preview, err)
	}
	if applied, err := service.ApplyReprice(ctx, owner, reprice); err != nil || applied.AffectedAttempts != 0 || applied.MissingPrices != 0 || applied.DeltaUSD != "0" {
		t.Fatalf("reprice apply=%#v err=%v", applied, err)
	}
	var assessments int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM cost_assessments WHERE attempt_id=?", admission.AttemptID).Scan(&assessments); err != nil || assessments != 0 {
		t.Fatalf("cost assessments=%d err=%v", assessments, err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var projectedCreation, projectedRead, projectedFive, projectedOne int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT cache_creation_input_tokens,cache_read_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens FROM usage_daily").Scan(&projectedCreation, &projectedRead, &projectedFive, &projectedOne); err != nil || projectedCreation != 20 || projectedRead != 30 || projectedFive != 15 || projectedOne != 5 {
		t.Fatalf("projected=%d/%d/%d/%d err=%v", projectedCreation, projectedRead, projectedFive, projectedOne, err)
	}
}

func TestPromptCacheAdmissionRejectsSpendPolicyWithoutMutation(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"})
	_, err := service.Admit(context.Background(), AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "messages", Scope: "chat:generate", Dialect: "anthropic", PriceUnavailable: true, EstimatedInputTokens: 10, EstimatedOutputTokens: 1, OutputBounded: true})
	var denial *Denial
	if !errors.As(err, &denial) || denial.Metric != "spend" {
		t.Fatalf("denial=%v", err)
	}
	var attempts int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestPromptCacheReconciliationRequiresExplicitCost(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "messages", Scope: "chat:generate", Dialect: "anthropic", TargetDialect: "anthropic", EstimatedInputTokens: 100, EstimatedOutputTokens: 10, OutputBounded: true})
	if err != nil {
		t.Fatal(err)
	}
	input, output, creation, read := int64(60), int64(4), int64(20), int64(30)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "cache-unknown", State: "interrupted_unknown", UsageStatus: "unknown", InputTokens: &input, OutputTokens: &output, CacheCreationInputTokens: &creation, CacheReadInputTokens: &read, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileUnknown(ctx, owner, admission.AttemptID, ReconciliationInput{InputTokens: &input, OutputTokens: &output, UsageStatus: "provider_reported", Reason: "cache rates unavailable", IdempotencyKey: "cache-reconcile"}); err != nil {
		t.Fatal(err)
	}
	var status string
	var recorded, restated sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,as_recorded_cost_nanos,restated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&status, &recorded, &restated); err != nil || status != "unknown" || recorded.Valid || restated.Valid {
		t.Fatalf("status=%s recorded=%v restated=%v err=%v", status, recorded, restated, err)
	}
}
