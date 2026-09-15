package usage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestAdmissionIsAtomicAndSettlementIsIdempotent(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	requestPolicy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: "lifetime", LimitUnits: 1})
	tokenPolicy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "month", LimitUnits: 100})
	spendPolicy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "user", ScopeID: owner.ID, Metric: "spend", Algorithm: "quota", Period: "hour", LimitUSD: "1"})
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "10000", OutputUSDPerMillion: "0", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan struct {
		admission Admission
		err       error
	}, 8)
	var ready sync.WaitGroup
	ready.Add(8)
	for range 8 {
		go func() {
			ready.Done()
			<-start
			admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 10})
			results <- struct {
				admission Admission
				err       error
			}{admission, err}
		}()
	}
	ready.Wait()
	close(start)
	var admitted Admission
	successes := 0
	for range 8 {
		result := <-results
		if result.err == nil {
			successes++
			admitted = result.admission
			continue
		}
		var denial *Denial
		if !errors.As(result.err, &denial) {
			t.Fatalf("unexpected denial: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("admissions = %d, want 1", successes)
	}
	if consumed, reserved, err := service.debugCounters(ctx, requestPolicy.ID); err != nil || consumed != 1 || reserved != 0 {
		t.Fatalf("request counter = %d/%d, %v", consumed, reserved, err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, tokenPolicy.ID); err != nil || consumed != 0 || reserved != 10 {
		t.Fatalf("token counter = %d/%d, %v", consumed, reserved, err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, spendPolicy.ID); err != nil || consumed != 0 || reserved != 100_000_000 {
		t.Fatalf("spend counter = %d/%d, %v", consumed, reserved, err)
	}

	inputTokens, outputTokens, actualCost := int64(3), int64(5), int64(50_000_000)
	settlement := SettlementInput{IdempotencyKey: "settle-once", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &inputTokens, OutputTokens: &outputTokens, CostNanos: &actualCost, FinalRequest: true}
	if err := service.Settle(ctx, admitted.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admitted.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	settlement.IdempotencyKey = "settle-again"
	if err := service.Settle(ctx, admitted.AttemptID, settlement); !errors.Is(err, ErrConflict) {
		t.Fatalf("second settlement = %v", err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, tokenPolicy.ID); err != nil || consumed != 8 || reserved != 0 {
		t.Fatalf("settled tokens = %d/%d, %v", consumed, reserved, err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, spendPolicy.ID); err != nil || consumed != actualCost || reserved != 0 {
		t.Fatalf("settled spend = %d/%d, %v", consumed, reserved, err)
	}
	limits, err := service.EffectiveLimits(ctx, owner, keyID, "conn_test")
	if err != nil {
		t.Fatal(err)
	}
	foundSpend := false
	for _, limit := range limits {
		if limit.PolicyID == spendPolicy.ID {
			foundSpend = true
		}
		if limit.PolicyID == spendPolicy.ID && (limit.LimitUSD != "1" || limit.ConsumedUSD != "0.05" || limit.ReservedUSD != "0" || limit.RemainingUSD != "0.95") {
			t.Fatalf("effective spend = %#v", limit)
		}
	}
	if !foundSpend {
		t.Fatal("effective spend policy missing")
	}
	if delivered, err := ProjectOutbox(ctx, store, 100); err != nil || delivered == 0 {
		t.Fatalf("project outbox = %d, %v", delivered, err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var projectedRequests, projectedTokens, projectedCost int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT requests, input_tokens + output_tokens, known_cost_nanos FROM usage_daily WHERE owner_user_id = ?", owner.ID).Scan(&projectedRequests, &projectedTokens, &projectedCost); err != nil || projectedRequests != 1 || projectedTokens != 8 || projectedCost != actualCost {
		t.Fatalf("projection = %d/%d/%d, %v", projectedRequests, projectedTokens, projectedCost, err)
	}
}

func TestTokenBucketUsesNondecreasingClock(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "token_bucket", LimitUnits: 2, RefillUnits: 1, RefillIntervalMS: 1000})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"}
	for range 2 {
		if _, err := service.Admit(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Admit(ctx, input); err == nil {
		t.Fatal("bucket over-admitted")
	}
	clock = clock.Add(time.Second)
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatalf("refill admission: %v", err)
	}
	clock = clock.Add(-time.Hour)
	if _, err := service.Admit(ctx, input); err == nil {
		t.Fatal("clock rollback granted refill")
	}
}

func TestTokenBucketCarriesFractionalRefill(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "token_bucket", LimitUnits: 2, RefillUnits: 1, RefillIntervalMS: 1000})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"}
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatalf("fractional refill was lost: %v", err)
	}
}

func TestCeilingDenialDoesNotConsumeSiblingPolicy(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	requests := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: "lifetime", LimitUnits: 1})
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "body_bytes", Algorithm: "ceiling", LimitUnits: 10})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", BodyBytes: 11}
	if _, err := service.Admit(ctx, input); err == nil {
		t.Fatal("oversized body was admitted")
	}
	if consumed, reserved, err := service.debugCounters(ctx, requests.ID); err != nil || consumed != 0 || reserved != 0 {
		t.Fatalf("denial changed request counter = %d/%d, %v", consumed, reserved, err)
	}
	input.BodyBytes = 10
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionRechecksKeyGrants(t *testing.T) {
	ctx, service, _, keyID, store := testService(t)
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE api_keys SET scopes_json = '[]' WHERE id = ?", keyID); err != nil {
		t.Fatal(err)
	}
	_, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("grant denial = %v", err)
	}
}

func TestAdmissionRechecksSelectedFreePrice(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	free, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "0", OutputUSDPerMillion: "0", Source: "test", EffectiveFrom: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE price_versions SET created_at=? WHERE id=?", time.Now().Add(-25*time.Hour).UnixMilli(), free.ID); err != nil {
		t.Fatal(err)
	}
	_, err = service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", RequireFreePrice: true, RequiredPriceVersionID: free.ID})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("stale free price admission = %v", err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE price_versions SET created_at=? WHERE id=?", time.Now().UnixMilli(), free.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", RequireFreePrice: true, RequiredPriceVersionID: free.ID})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("changed free price admission = %v", err)
	}
}

func TestCalendarQuotaResetsAtUTCBoundary(t *testing.T) {
	for name, period := range map[string]string{"week": "week", "month": "month"} {
		t.Run(name, func(t *testing.T) {
			ctx, service, owner, keyID, store := testService(t)
			defer store.Close()
			clock := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
			if period == "week" {
				clock = time.Date(2026, 1, 4, 23, 59, 59, 0, time.UTC)
			}
			service.now = func() time.Time { return clock }
			createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: period, LimitUnits: 1})
			input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"}
			if _, err := service.Admit(ctx, input); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Admit(ctx, input); err == nil {
				t.Fatal("quota over-admitted before boundary")
			}
			clock = clock.Add(time.Second)
			if _, err := service.Admit(ctx, input); err != nil {
				t.Fatalf("quota did not reset: %v", err)
			}
		})
	}
}

func TestLoweredCapBlocksNewReservations(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 20})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 10}
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePolicy(ctx, owner, policy.ID, policy.Revision, 5, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Admit(ctx, input); err == nil {
		t.Fatal("lowered cap admitted new reservation")
	}
}

func TestPolicyUpdateRejectsDashboardUnsafeInteger(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: "lifetime", LimitUnits: 10})
	if _, err := service.UpdatePolicy(ctx, owner, policy.ID, policy.Revision, maxSafeInteger+1, "", true); err == nil {
		t.Fatal("unsafe integer limit was accepted")
	}
}

func TestFixedWindowResetsAtBoundary(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.UnixMilli(1_000).UTC()
	service.now = func() time.Time { return clock }
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "fixed_window", WindowSeconds: 1, LimitUnits: 1})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"}
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Admit(ctx, input); err == nil {
		t.Fatal("fixed window over-admitted")
	}
	clock = clock.Add(time.Second)
	if _, err := service.Admit(ctx, input); err != nil {
		t.Fatalf("fixed window did not reset: %v", err)
	}
}

func TestFallbackCountsOneLogicalRequestAndEachAttempt(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	logical := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: "lifetime", LimitUnits: 1})
	connection := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "connection", ScopeID: "conn_test", Metric: "requests", Algorithm: "quota", Period: "lifetime", LimitUnits: 2})
	tokens := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 20})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 5}
	first, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	five := int64(5)
	if err := service.Settle(ctx, first.AttemptID, SettlementInput{IdempotencyKey: "fallback-first", State: "failed", UsageStatus: "provider_reported", InputTokens: &five, OutputTokens: new(int64), FinalRequest: false}); err != nil {
		t.Fatal(err)
	}
	input.RequestID = first.RequestID
	second, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, second.AttemptID, SettlementInput{IdempotencyKey: "fallback-second", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &five, OutputTokens: new(int64), CostNanos: new(int64), FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	if consumed, _, _ := service.debugCounters(ctx, logical.ID); consumed != 1 {
		t.Fatalf("logical requests = %d", consumed)
	}
	if consumed, _, _ := service.debugCounters(ctx, connection.ID); consumed != 2 {
		t.Fatalf("connection attempts = %d", consumed)
	}
	if consumed, reserved, _ := service.debugCounters(ctx, tokens.ID); consumed != 10 || reserved != 0 {
		t.Fatalf("attempt tokens = %d/%d", consumed, reserved)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var projectedRequests int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT SUM(requests) FROM usage_daily").Scan(&projectedRequests); err != nil || projectedRequests != 1 {
		t.Fatalf("projected logical requests = %d, %v", projectedRequests, err)
	}
}

func TestFullOutboxStopsAdmissionBeforeAccounting(t *testing.T) {
	ctx, service, _, keyID, store := testService(t)
	defer store.Close()
	_, err := store.SystemDB().ExecContext(ctx, `WITH RECURSIVE seq(value) AS (VALUES(1) UNION ALL SELECT value + 1 FROM seq WHERE value < 10000)
		INSERT INTO event_outbox (id, event_type, payload_json, created_at) SELECT printf('evt_%05d', value), 'test', '{}', value FROM seq`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"})
	if err == nil || !strings.Contains(err.Error(), "outbox is full") {
		t.Fatalf("admission error = %v", err)
	}
	var requests int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM requests").Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("requests after denial = %d, %v", requests, err)
	}
}

func TestCancelBeforeDispatchReleasesReservations(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "requests", Algorithm: "quota", Period: "day", LimitUnits: 1})
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1})
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "connection", ScopeID: "conn_test", Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1})
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CancelBeforeDispatch(ctx, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	var state string
	var active, reserved, leases int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT state FROM attempts WHERE id=?", admission.AttemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM reservations WHERE attempt_id=? AND state='active'", admission.AttemptID).Scan(&active)
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COALESCE(SUM(reserved_units),0) FROM quota_periods").Scan(&reserved)
	_ = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM concurrency_leases WHERE request_id=?", admission.RequestID).Scan(&leases)
	if state != "cancelled_before_dispatch" || active != 0 || reserved != 0 || leases != 0 {
		t.Fatalf("cancel state=%s active=%d reserved=%d leases=%d", state, active, reserved, leases)
	}
}

func TestRecoveryReleasesUndispatchedAndPreservesUnknownReservations(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 100})
	input := AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 25}
	undispatched, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var cancelledRequests int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT SUM(requests) FROM usage_daily").Scan(&cancelledRequests); err != nil || cancelledRequests != 1 {
		t.Fatalf("projected cancelled request = %d, %v", cancelledRequests, err)
	}
	if consumed, reserved, _ := service.debugCounters(ctx, policy.ID); consumed != 0 || reserved != 0 {
		t.Fatalf("released counter = %d/%d", consumed, reserved)
	}
	if _, err := service.database.ExecContext(ctx, "DELETE FROM requests WHERE id = ?", undispatched.RequestID); err == nil {
		t.Fatal("authoritative request history was deletable")
	}

	dispatched, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkDispatching(ctx, dispatched.AttemptID); err != nil {
		t.Fatal(err)
	}
	service.processEpoch = "ep_restart"
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if consumed, reserved, _ := service.debugCounters(ctx, policy.ID); consumed != 0 || reserved != 25 {
		t.Fatalf("unknown counter = %d/%d", consumed, reserved)
	}
	var state string
	if err := service.database.QueryRowContext(ctx, "SELECT state FROM reservations WHERE attempt_id = ?", dispatched.AttemptID).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("reservation state = %q, %v", state, err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err := service.ReconcileUnknown(ctx, owner, dispatched.AttemptID, ReconciliationInput{InputTokens: &zero, OutputTokens: &zero, CostNanos: &zero, UsageStatus: "estimated", Reason: "confirmed unbilled", IdempotencyKey: "reconcile-unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileUnknown(ctx, owner, dispatched.AttemptID, ReconciliationInput{InputTokens: &zero, OutputTokens: &zero, CostNanos: &zero, UsageStatus: "estimated", Reason: "confirmed unbilled", IdempotencyKey: "reconcile-unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var unknown int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT SUM(unknown_attempts) FROM usage_daily").Scan(&unknown); err != nil || unknown != 0 {
		t.Fatalf("unknown projection = %d, %v", unknown, err)
	}
}

func TestFinalizeRequestReleasesFallbackConcurrency(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "concurrency", Algorithm: "concurrency", LimitUnits: 1})
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "fallback-terminal", State: "failed", UsageStatus: "provider_reported", InputTokens: &zero, OutputTokens: &zero, CostNanos: &zero, FinalRequest: false}); err != nil {
		t.Fatal(err)
	}
	if err := service.FinalizeRequest(ctx, admission.RequestID, "failed"); err != nil {
		t.Fatal(err)
	}
	if consumed, _, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 0 {
		t.Fatalf("concurrency = %d, %v", consumed, err)
	}
}

func TestReconciliationRepairsUnknownAndPartialSettlements(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 100})
	zero, three, two := int64(0), int64(3), int64(2)

	unknown, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_unknown", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, unknown.AttemptID, SettlementInput{IdempotencyKey: "unknown-success", State: "succeeded", UsageStatus: "unknown", CostNanos: &zero, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileUnknown(ctx, owner, unknown.AttemptID, ReconciliationInput{InputTokens: &zero, OutputTokens: &zero, UsageStatus: "estimated", Reason: "provider omitted usage", IdempotencyKey: "repair-unknown-success"}); err != nil {
		t.Fatal(err)
	}

	partial, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_partial", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, partial.AttemptID, SettlementInput{IdempotencyKey: "partial-known", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &three, CostNanos: &zero, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileUnknown(ctx, owner, partial.AttemptID, ReconciliationInput{InputTokens: &three, OutputTokens: &two, UsageStatus: "provider_reported", Reason: "provider supplied final tokens", IdempotencyKey: "repair-partial-known"}); err != nil {
		t.Fatal(err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 5 || reserved != 0 {
		t.Fatalf("reconciled counter = %d/%d, %v", consumed, reserved, err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var unknownCount int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT COALESCE(SUM(unknown_attempts), 0) FROM usage_daily").Scan(&unknownCount); err != nil || unknownCount != 0 {
		t.Fatalf("projected unknown attempts = %d, %v", unknownCount, err)
	}
}

func TestSettlementRejectsOverflowAndMismatchedRetry(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: maxSafeInteger})
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE quota_periods SET consumed_units = ? WHERE policy_id = ?", maxSafeInteger, policy.ID); err != nil {
		t.Fatal(err)
	}
	one, zero := int64(1), int64(0)
	input := SettlementInput{IdempotencyKey: "overflow-settlement", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &one, OutputTokens: &zero, CostNanos: &zero, FinalRequest: true}
	if err := service.Settle(ctx, admission.AttemptID, input); err == nil {
		t.Fatal("overflow settlement succeeded")
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE quota_periods SET consumed_units = 0 WHERE policy_id = ?", policy.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admission.AttemptID, input); err != nil {
		t.Fatal(err)
	}
	input.State = "failed"
	if err := service.Settle(ctx, admission.AttemptID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched retry = %v", err)
	}
}

func testService(t *testing.T) (context.Context, *Service, auth.User, string, *storage.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	owner := auth.User{ID: "usr_owner", Email: "owner@example.test", DisplayName: "Owner", Role: "owner", Status: "active"}
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id, email, display_name, password_hash, role, status, inference_unrestricted, created_at, updated_at) VALUES (?, ?, ?, 'hash', 'owner', 'active', 1, ?, ?)", owner.ID, owner.Email, owner.DisplayName, now, now); err != nil {
		t.Fatal(err)
	}
	keyID := "key_test"
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys (id, owner_user_id, label, state, scopes_json, model_patterns_json, connection_ids_json, created_at, updated_at) VALUES (?, ?, 'Test', 'active', '["chat:generate"]', '["model_*"]', '["conn_test"]', ?, ?)`, keyID, owner.ID, now, now); err != nil {
		t.Fatal(err)
	}
	return ctx, New(store.SystemDB()), owner, keyID, store
}

func createPolicy(t *testing.T, ctx context.Context, service *Service, owner auth.User, input PolicyInput) Policy {
	t.Helper()
	policy, err := service.CreatePolicy(ctx, owner, input)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
