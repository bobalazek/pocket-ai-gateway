package usage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestWebSearchSettlementIsUnpricedBoundedAndProjectedOnce(t *testing.T) {
	ctx, service, owner, keyID, store := testWebSearchService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 1000})
	price, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", TargetOperation: "responses", Scope: "responses:generate", Dialect: "responses", TargetDialect: "openai", WebSearchMaxCalls: 2, EstimatedInputTokens: 100, EstimatedOutputTokens: 10, OutputBounded: true})
	if err != nil {
		t.Fatal(err)
	}
	var admittedMax int64
	var priceID sql.NullString
	var estimated sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT web_search_max_calls,price_version_id,estimated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&admittedMax, &priceID, &estimated); err != nil || admittedMax != 2 || priceID.Valid || estimated.Valid {
		t.Fatalf("admission max=%d price=%v estimate=%v err=%v", admittedMax, priceID, estimated, err)
	}
	// A restored or future row may have both the hosted marker and a price snapshot.
	// Settlement must still avoid the ordinary two-rate calculation.
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE attempts SET price_version_id=? WHERE id=?", price.ID, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	input, output, calls := int64(60), int64(4), int64(1)
	settlement := SettlementInput{IdempotencyKey: "web-search-settle", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, WebSearchCallCount: &calls, FinalRequest: true}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatalf("idempotent settlement: %v", err)
	}
	changed := int64(2)
	settlement.WebSearchCallCount = &changed
	if err := service.Settle(ctx, admission.AttemptID, settlement); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed idempotent settlement = %v", err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 64 || reserved != 0 {
		t.Fatalf("tokens=%d/%d err=%v", consumed, reserved, err)
	}
	var recorded, restated sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT as_recorded_cost_nanos,restated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&recorded, &restated); err != nil || recorded.Valid || restated.Valid {
		t.Fatalf("recorded=%v restated=%v err=%v", recorded, restated, err)
	}
	var payload string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT payload_json FROM event_outbox WHERE event_type='attempt.settled' AND attempt_id=?", admission.AttemptID).Scan(&payload); err != nil || !strings.Contains(payload, `"web_search_call_count":1`) || strings.Contains(payload, "query") || strings.Contains(payload, "source") || strings.Contains(payload, "citation") {
		t.Fatalf("settlement payload=%q err=%v", payload, err)
	}
	if delivered, err := ProjectOutbox(ctx, store, 100); err != nil || delivered == 0 {
		t.Fatalf("project=%d err=%v", delivered, err)
	}
	if delivered, err := ProjectOutbox(ctx, store, 100); err != nil || delivered != 0 {
		t.Fatalf("reproject=%d err=%v", delivered, err)
	}
	var projected int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT web_search_calls FROM usage_daily").Scan(&projected); err != nil || projected != 1 {
		t.Fatalf("projected=%d err=%v", projected, err)
	}
	summary, err := service.Summary(ctx, owner, UsageQuery{})
	if err != nil || summary.WebSearchCalls != 1 || len(summary.Points) != 1 || summary.Points[0].WebSearchCalls != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	requests, _, err := service.ListRequests(ctx, owner, UsageQuery{})
	if err != nil || len(requests) != 1 || len(requests[0].Attempts) != 1 || requests[0].Attempts[0].WebSearchMaxCalls == nil || *requests[0].Attempts[0].WebSearchMaxCalls != 2 || requests[0].Attempts[0].WebSearchCallCount == nil || *requests[0].Attempts[0].WebSearchCallCount != 1 {
		t.Fatalf("requests=%#v err=%v", requests, err)
	}
	reprice := RepriceInput{ConnectionID: "conn_test", ModelID: "model_test", From: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), To: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), IdempotencyKey: "web-search-reprice"}
	if preview, err := service.PreviewReprice(ctx, owner, reprice); err != nil || preview.AffectedAttempts != 0 || preview.MissingPrices != 0 || preview.DeltaUSD != "0" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if applied, err := service.ApplyReprice(ctx, owner, reprice); err != nil || applied.AffectedAttempts != 0 || applied.MissingPrices != 0 || applied.DeltaUSD != "0" {
		t.Fatalf("apply=%#v err=%v", applied, err)
	}
}

func TestStreamingAnthropicWebSearchUsesSharedAccountingContract(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	policy := createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "tokens", Algorithm: "quota", Period: "lifetime", LimitUnits: 1000})
	price, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "messages", TargetOperation: "messages", Scope: "chat:generate", Dialect: "anthropic", TargetDialect: "anthropic", WebSearchMaxCalls: 4, EstimatedInputTokens: 80, EstimatedOutputTokens: 20, OutputBounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 0 || reserved != 100 {
		t.Fatalf("admitted tokens=%d/%d err=%v", consumed, reserved, err)
	}
	var admittedMax int64
	var priceID sql.NullString
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT web_search_max_calls,price_version_id FROM attempts WHERE id=?", admission.AttemptID).Scan(&admittedMax, &priceID); err != nil || admittedMax != 4 || priceID.Valid {
		t.Fatalf("admission max=%d price=%v err=%v", admittedMax, priceID, err)
	}
	// Even a restored row with a base token price must retain unknown hosted-search cost.
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE attempts SET price_version_id=? WHERE id=?", price.ID, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkDispatching(ctx, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkStreaming(ctx, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	input, output, calls := int64(70), int64(15), int64(3)
	settlement := SettlementInput{IdempotencyKey: "anthropic-web-search-settle", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, WebSearchCallCount: &calls, FinalRequest: true}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatalf("idempotent settlement: %v", err)
	}
	if consumed, reserved, err := service.debugCounters(ctx, policy.ID); err != nil || consumed != 85 || reserved != 0 {
		t.Fatalf("settled tokens=%d/%d err=%v", consumed, reserved, err)
	}
	if delivered, err := ProjectOutbox(ctx, store, 100); err != nil || delivered == 0 {
		t.Fatalf("project=%d err=%v", delivered, err)
	}
	var projected int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT web_search_calls FROM usage_daily").Scan(&projected); err != nil || projected != 3 {
		t.Fatalf("projected=%d err=%v", projected, err)
	}
	summary, err := service.Summary(ctx, owner, UsageQuery{Dialect: "anthropic"})
	if err != nil || summary.WebSearchCalls != 3 || len(summary.Points) != 1 || summary.Points[0].WebSearchCalls != 3 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	requests, _, err := service.ListRequests(ctx, owner, UsageQuery{Dialect: "anthropic"})
	if err != nil || len(requests) != 1 || len(requests[0].Attempts) != 1 {
		t.Fatalf("requests=%#v err=%v", requests, err)
	}
	attempt := requests[0].Attempts[0]
	if attempt.TargetDialect != "anthropic" || attempt.TargetOperation != "messages" || attempt.WebSearchMaxCalls == nil || *attempt.WebSearchMaxCalls != 4 || attempt.WebSearchCallCount == nil || *attempt.WebSearchCallCount != 3 || attempt.CostUSD != nil {
		t.Fatalf("attempt=%#v", attempt)
	}
	reprice := RepriceInput{ConnectionID: "conn_test", ModelID: "model_test", From: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), To: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), IdempotencyKey: "anthropic-web-search-reprice"}
	if preview, err := service.PreviewReprice(ctx, owner, reprice); err != nil || preview.AffectedAttempts != 0 || preview.MissingPrices != 0 || preview.DeltaUSD != "0" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
}

func TestWebSearchSettlementRequiresExactTerminalCount(t *testing.T) {
	tests := []struct {
		name      string
		max       int64
		state     string
		usage     string
		calls     *int64
		wantError bool
	}{
		{name: "ordinary rejects count", state: "succeeded", usage: "provider_reported", calls: int64Pointer(0), wantError: true},
		{name: "hosted success requires count", max: 2, state: "succeeded", usage: "provider_reported", wantError: true},
		{name: "hosted success bounds count", max: 2, state: "succeeded", usage: "provider_reported", calls: int64Pointer(3), wantError: true},
		{name: "hosted failure requires nil", max: 2, state: "failed", usage: "unknown", calls: int64Pointer(1), wantError: true},
		{name: "hosted unknown success requires nil", max: 2, state: "succeeded", usage: "unknown", calls: int64Pointer(0), wantError: true},
		{name: "hosted failure accepts nil", max: 2, state: "failed", usage: "unknown"},
		{name: "hosted known success accepts zero", max: 2, state: "succeeded", usage: "provider_reported", calls: int64Pointer(0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, service, _, keyID, store := testWebSearchService(t)
			defer store.Close()
			admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", TargetOperation: "responses", Scope: "responses:generate", Dialect: "responses", TargetDialect: "openai", WebSearchMaxCalls: test.max, EstimatedInputTokens: 1, EstimatedOutputTokens: 1, OutputBounded: true})
			if err != nil {
				t.Fatal(err)
			}
			input, output := int64(1), int64(1)
			err = service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "terminal-count", State: test.state, UsageStatus: test.usage, InputTokens: &input, OutputTokens: &output, WebSearchCallCount: test.calls, FinalRequest: true})
			if test.wantError && err == nil || !test.wantError && err != nil {
				t.Fatalf("settle error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestWebSearchAdmissionRejectsSpendAndReconciliationDoesNotAutoPrice(t *testing.T) {
	ctx, service, owner, keyID, store := testWebSearchService(t)
	defer store.Close()
	createPolicy(t, ctx, service, owner, PolicyInput{ScopeKind: "key", ScopeID: keyID, Metric: "spend", Algorithm: "quota", Period: "lifetime", LimitUSD: "1"})
	if _, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", TargetOperation: "responses", Scope: "responses:generate", Dialect: "responses", TargetDialect: "openai", WebSearchMaxCalls: 1, EstimatedInputTokens: 1, EstimatedOutputTokens: 1, OutputBounded: true}); err == nil {
		t.Fatal("hosted search was admitted under a spend policy")
	}
	if _, err := store.SystemDB().ExecContext(ctx, "DELETE FROM limit_policies"); err != nil {
		t.Fatal(err)
	}
	price, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "test", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", TargetOperation: "responses", Scope: "responses:generate", Dialect: "responses", TargetDialect: "openai", WebSearchMaxCalls: 1, EstimatedInputTokens: 10, EstimatedOutputTokens: 1, OutputBounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE attempts SET price_version_id=? WHERE id=?", price.ID, admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "web-search-unknown", State: "interrupted_unknown", UsageStatus: "unknown", FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	input, output := int64(5), int64(2)
	if err := service.ReconcileUnknown(ctx, owner, admission.AttemptID, ReconciliationInput{InputTokens: &input, OutputTokens: &output, UsageStatus: "provider_reported", Reason: "hosted search cost unavailable", IdempotencyKey: "web-search-reconcile"}); err != nil {
		t.Fatal(err)
	}
	var status string
	var recorded, restated sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT usage_status,as_recorded_cost_nanos,restated_cost_nanos FROM attempts WHERE id=?", admission.AttemptID).Scan(&status, &recorded, &restated); err != nil || status != "unknown" || recorded.Valid || restated.Valid {
		t.Fatalf("status=%s recorded=%v restated=%v err=%v", status, recorded, restated, err)
	}
}

func TestWebSearchAdmissionBounds(t *testing.T) {
	_, service, _, keyID, store := testWebSearchService(t)
	defer store.Close()
	for _, maxCalls := range []int64{-1, 5} {
		if _, err := service.Admit(t.Context(), AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "responses", TargetOperation: "responses", Scope: "responses:generate", Dialect: "responses", TargetDialect: "openai", WebSearchMaxCalls: maxCalls}); err == nil {
			t.Fatalf("max calls %d accepted", maxCalls)
		}
	}
}

func int64Pointer(value int64) *int64 { return &value }

func testWebSearchService(t *testing.T) (context.Context, *Service, auth.User, string, *storage.Store) {
	t.Helper()
	ctx, service, owner, keyID, store := testService(t)
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE api_keys SET scopes_json='["responses:generate"]' WHERE id=?`, keyID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return ctx, service, owner, keyID, store
}
