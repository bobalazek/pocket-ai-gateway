package usage

import (
	"database/sql"
	"testing"
	"time"
)

func TestHistoricalRepricingIsEffectiveDatedAndIdempotent(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	inputTokens, outputTokens := int64(10), int64(5)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "unknown-settlement", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &inputTokens, OutputTokens: &outputTokens, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	price, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", Source: "operator test price", EffectiveFrom: clock.Add(-time.Hour).Format(time.RFC3339), EffectiveTo: clock.Add(time.Hour).Format(time.RFC3339)})
	if err != nil || price.ID == "" {
		t.Fatalf("price = %#v, %v", price, err)
	}
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "overlap", EffectiveFrom: clock.Format(time.RFC3339), EffectiveTo: clock.Add(2 * time.Hour).Format(time.RFC3339)}); err == nil {
		t.Fatal("overlapping price succeeded")
	}

	request := RepriceInput{ConnectionID: "conn_test", ModelID: "model_test", From: clock.Add(-time.Hour).Format(time.RFC3339), To: clock.Add(time.Hour).Format(time.RFC3339), IdempotencyKey: "reprice-job-once"}
	preview, err := service.PreviewReprice(ctx, owner, request)
	if err != nil || preview.AffectedAttempts != 1 || preview.MissingPrices != 0 || preview.DeltaUSD != "0.00004" {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	for range 2 {
		applied, err := service.ApplyReprice(ctx, owner, request)
		if err != nil || applied.AffectedAttempts != 1 || applied.DeltaUSD != "0.00004" {
			t.Fatalf("apply = %#v, %v", applied, err)
		}
	}
	if _, err := ProjectOutbox(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	var cost, assessments, ledger int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT restated_cost_nanos FROM attempts WHERE id = ?", admission.AttemptID).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM cost_assessments WHERE attempt_id = ?", admission.AttemptID).Scan(&assessments); err != nil {
		t.Fatal(err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_ledger WHERE attempt_id = ? AND entry_type = 'reprice'", admission.AttemptID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if cost != 40_000 || assessments != 1 || ledger != 1 {
		t.Fatalf("repriced state = cost %d assessments %d ledger %d", cost, assessments, ledger)
	}
	var projectedCost int64
	if err := store.DataDB().QueryRowContext(ctx, "SELECT known_cost_nanos FROM usage_daily WHERE owner_user_id = ?", owner.ID).Scan(&projectedCost); err != nil || projectedCost != 40_000 {
		t.Fatalf("projected repriced cost = %d, %v", projectedCost, err)
	}
}

func TestPinnedPriceCalculatesSettlementAndSuccessorClosesSafely(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	first, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", Source: "initial", EffectiveFrom: clock.Add(-time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai", EstimatedInputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	in, out := int64(10), int64(5)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "priced-settlement", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &in, OutputTokens: &out, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	var cost int64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT as_recorded_cost_nanos FROM attempts WHERE id = ?", admission.AttemptID).Scan(&cost); err != nil || cost != 40_000 {
		t.Fatalf("calculated cost = %d, %v", cost, err)
	}
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "3", OutputUSDPerMillion: "5", Source: "successor", EffectiveFrom: clock.Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var closed sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT effective_to FROM price_versions WHERE id = ?", first.ID).Scan(&closed); err != nil || !closed.Valid || closed.Int64 != clock.Add(time.Hour).UnixMilli() {
		t.Fatalf("closed interval = %#v, %v", closed, err)
	}
}

func TestPriceSuccessorCannotInvalidatePinnedAttempt(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", Source: "initial", EffectiveFrom: clock.Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "3", OutputUSDPerMillion: "5", Source: "retroactive", EffectiveFrom: clock.Add(-time.Minute).Format(time.RFC3339)}); err == nil {
		t.Fatal("retroactive successor invalidated a snapshot")
	}
}
