package usage

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestScheduledCacheReadPriceQuoteUsesUTCHalfOpenWindow(t *testing.T) {
	ctx, service, owner, _, store := testService(t)
	defer store.Close()
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cacheRate := "0.5"
	firstStart, firstEnd, secondStart, secondEnd := int64(0), int64(60), int64(60), int64(120)
	first, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", CacheReadUSDPerMillion: &cacheRate, Source: "scheduled", EffectiveFrom: from.Format(time.RFC3339), WeeklyStartMinuteUTC: &firstStart, WeeklyEndMinuteUTC: &firstEnd})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "3", OutputUSDPerMillion: "5", CacheReadUSDPerMillion: &cacheRate, Source: "scheduled", EffectiveFrom: from.Format(time.RFC3339), WeeklyStartMinuteUTC: &secondStart, WeeklyEndMinuteUTC: &secondEnd})
	if err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 3, 2, 0, 59, 59, 0, time.FixedZone("offset", 2*60*60))
	quote, err := QuotePrice(ctx, store.SystemDB(), "conn_test", "model_test", monday)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local-time window was not converted to UTC: quote=%#v err=%v", quote, err)
	}
	// 00:59 at UTC+02 is Sunday UTC; use a UTC boundary to prove the half-open handoff.
	mondayUTC := time.Date(2026, 3, 2, 0, 59, 59, 0, time.UTC)
	quote, err = service.QuotePrice(ctx, "conn_test", "model_test", mondayUTC)
	if err != nil || quote.ID != first.ID {
		t.Fatalf("UTC first quote=%#v err=%v", quote, err)
	}
	quote, err = service.QuotePrice(ctx, "conn_test", "model_test", mondayUTC.Add(time.Second))
	if err != nil || quote.ID != second.ID {
		t.Fatalf("UTC second quote=%#v err=%v", quote, err)
	}
	cost, err := quote.EstimateCost(100, 10, 20)
	if err != nil || cost == nil || *cost != 300_000 {
		t.Fatalf("cache-aware cost=%v err=%v", cost, err)
	}
	cost, err = (PriceQuote{InputNanosPerMillion: 3_000_000_000, OutputNanosPerMillion: 5_000_000_000}).EstimateCost(100, 10, 20)
	if err != nil || cost != nil {
		t.Fatalf("missing cache rate cost=%v err=%v", cost, err)
	}
	if _, err := service.QuotePrice(ctx, "conn_test", "model_test", mondayUTC.Add(2*time.Hour)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("outside quote error=%v", err)
	}
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "1", OutputUSDPerMillion: "1", Source: "overlap", EffectiveFrom: from.Format(time.RFC3339), WeeklyStartMinuteUTC: &firstStart, WeeklyEndMinuteUTC: &secondEnd}); err == nil {
		t.Fatal("overlapping weekly price succeeded")
	}
}

func TestCacheReadSettlementUsesVersionTwoProvenance(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	cacheRate := "0.5"
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", CacheReadUSDPerMillion: &cacheRate, Source: "cache-aware", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", TargetOperation: "chat/completions", Scope: "chat:generate", Dialect: "openai", TargetDialect: "openai", EstimatedInputTokens: 100, EstimatedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	input, output, read := int64(100), int64(10), int64(20)
	settlement := SettlementInput{IdempotencyKey: "cache-read-price", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, CacheReadInputTokens: &read, FinalRequest: true}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatal(err)
	}
	if err := service.Settle(ctx, admission.AttemptID, settlement); err != nil {
		t.Fatalf("idempotent settlement=%v", err)
	}
	var cost, version int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT attempts.as_recorded_cost_nanos,cost_assessments.calculation_version FROM attempts JOIN cost_assessments ON cost_assessments.attempt_id=attempts.id WHERE attempts.id=?`, admission.AttemptID).Scan(&cost, &version); err != nil || cost != 210_000 || version != 2 {
		t.Fatalf("cost=%d version=%d err=%v", cost, version, err)
	}
}

func TestCacheReadRepricingUsesVersionTwoProvenance(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 3, 2, 0, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	admission, err := service.Admit(ctx, AdmissionInput{KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", TargetOperation: "chat/completions", Scope: "chat:generate", Dialect: "openai", TargetDialect: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	input, output, read := int64(100), int64(10), int64(20)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "cache-read-unpriced", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, CacheReadInputTokens: &read, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	cacheRate := "0.5"
	if _, err := service.CreatePrice(ctx, owner, PriceInput{ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4", CacheReadUSDPerMillion: &cacheRate, Source: "correction", EffectiveFrom: clock.Add(-time.Hour).Format(time.RFC3339), EffectiveTo: clock.Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	request := RepriceInput{ConnectionID: "conn_test", ModelID: "model_test", From: clock.Add(-time.Hour).Format(time.RFC3339), To: clock.Add(time.Hour).Format(time.RFC3339), IdempotencyKey: "cache-read-reprice"}
	preview, err := service.PreviewReprice(ctx, owner, request)
	if err != nil || preview.AffectedAttempts != 1 || preview.DeltaUSD != "0.00021" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if _, err := service.ApplyReprice(ctx, owner, request); err != nil {
		t.Fatal(err)
	}
	var amount, version int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT amount_nanos,calculation_version FROM cost_assessments WHERE attempt_id=? AND kind='restated'`, admission.AttemptID).Scan(&amount, &version); err != nil || amount != 210_000 || version != 2 {
		t.Fatalf("amount=%d version=%d err=%v", amount, version, err)
	}
}

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
