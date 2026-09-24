package usage

import (
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

func TestListRequestsFiltersExactIDAndReturnsPriceProvenance(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	clock := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	cacheReadRate, webSearchFee := "0", "0.01"
	price, err := service.CreatePrice(ctx, owner, PriceInput{
		ConnectionID: "conn_test", ModelID: "model_test", InputUSDPerMillion: "2", OutputUSDPerMillion: "4",
		CacheReadUSDPerMillion: &cacheReadRate, WebSearchUSDPerCall: &webSearchFee,
		Source: "provider price page", EffectiveFrom: clock.Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := service.Admit(ctx, AdmissionInput{
		KeyID: keyID, ConnectionID: "conn_test", ModelID: "model_test", Operation: "chat", Scope: "chat:generate", Dialect: "openai",
		EstimatedInputTokens: 10, EstimatedOutputTokens: 5, OutputBounded: true, RejectedCandidatesJSON: "null",
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedRejected string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT rejected_candidates_json FROM attempts WHERE id=?", admission.AttemptID).Scan(&storedRejected); err != nil || storedRejected != "[]" {
		t.Fatalf("new attempt stored rejected candidates = %q, err=%v", storedRejected, err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE attempts SET rejected_candidates_json='null' WHERE id=?", admission.AttemptID); err != nil {
		t.Fatal(err)
	}
	pending, _, err := service.ListRequests(ctx, owner, UsageQuery{RequestID: admission.RequestID})
	if err != nil || len(pending) != 1 || pending[0].Attempts[0].InputTokens != nil || pending[0].Attempts[0].OutputTokens != nil {
		t.Fatalf("pending usage was not null: %#v err=%v", pending, err)
	}
	if pending[0].Attempts[0].RejectedCandidates == nil {
		t.Fatal("empty rejected candidates must be an array, not null")
	}
	input, output := int64(3), int64(5)
	if err := service.Settle(ctx, admission.AttemptID, SettlementInput{IdempotencyKey: "request-detail", State: "succeeded", UsageStatus: "provider_reported", InputTokens: &input, OutputTokens: &output, FinalRequest: true}); err != nil {
		t.Fatal(err)
	}
	settled, _, err := service.ListRequests(ctx, owner, UsageQuery{RequestID: admission.RequestID})
	if err != nil || len(settled) != 1 || settled[0].Attempts[0].RestatedCostUSD != nil || settled[0].Attempts[0].RestatedPrice != nil {
		t.Fatalf("ordinary settlement reported a restatement: %#v err=%v", settled, err)
	}
	now := clock.UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO price_versions (id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,web_search_nanos_per_call,source,effective_from,created_by,created_at) VALUES ('prc_restatement','conn_test','model_test',3000000000,5000000000,0,'corrected provider price',?,?,?)`, now, owner.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO cost_assessments (id,attempt_id,price_version_id,calculation_version,kind,amount_nanos,delta_nanos,created_at) VALUES ('ass_restatement',?,'prc_restatement',1,'restated',30000,4000,?)`, admission.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE attempts SET restated_cost_nanos=30000 WHERE id=?", admission.AttemptID); err != nil {
		t.Fatal(err)
	}

	items, next, err := service.ListRequests(ctx, owner, UsageQuery{RequestID: admission.RequestID})
	if err != nil || next != "" || len(items) != 1 || len(items[0].Attempts) != 1 {
		t.Fatalf("requests=%#v next=%q err=%v", items, next, err)
	}
	attempt := items[0].Attempts[0]
	if attempt.EstimatedCostUSD == nil || *attempt.EstimatedCostUSD != "0.00004" || attempt.AsRecordedCostUSD == nil || *attempt.AsRecordedCostUSD != "0.000026" || attempt.RestatedCostUSD == nil || *attempt.RestatedCostUSD != "0.00003" || attempt.CostUSD == nil || *attempt.CostUSD != "0.00003" {
		t.Fatalf("costs=%#v", attempt)
	}
	if attempt.RecordedPrice == nil || attempt.RecordedPrice.ID != price.ID || attempt.RecordedPrice.Source != "provider price page" || attempt.RestatedPrice == nil || attempt.RestatedPrice.ID != "prc_restatement" || attempt.RestatedPrice.Source != "corrected provider price" {
		t.Fatalf("prices=%#v/%#v", attempt.RecordedPrice, attempt.RestatedPrice)
	}
	if attempt.RecordedPrice.CacheReadUSDPerMillion == nil || *attempt.RecordedPrice.CacheReadUSDPerMillion != "0" || attempt.RecordedPrice.WebSearchUSDPerCall == nil || *attempt.RecordedPrice.WebSearchUSDPerCall != "0.01" || attempt.RestatedPrice.CacheReadUSDPerMillion != nil || attempt.RestatedPrice.WebSearchUSDPerCall == nil || *attempt.RestatedPrice.WebSearchUSDPerCall != "0" {
		t.Fatalf("price rates=%#v/%#v", attempt.RecordedPrice, attempt.RestatedPrice)
	}
	if _, err := service.AdjustCost(ctx, owner, admission.AttemptID, "0.000001", "manual correction", "request-detail-adjustment"); err != nil {
		t.Fatal(err)
	}
	adjusted, _, err := service.ListRequests(ctx, owner, UsageQuery{RequestID: admission.RequestID})
	if err != nil || adjusted[0].Attempts[0].RestatedCostUSD == nil || *adjusted[0].Attempts[0].RestatedCostUSD != "0.00003" || adjusted[0].Attempts[0].CostUSD == nil || *adjusted[0].Attempts[0].CostUSD != "0.000031" || adjusted[0].Attempts[0].RestatedPrice == nil || adjusted[0].Attempts[0].RestatedPrice.ID != "prc_restatement" {
		t.Fatalf("adjusted request=%#v err=%v", adjusted, err)
	}
	if other, _, err := service.ListRequests(ctx, owner, UsageQuery{RequestID: "req_missing"}); err != nil || len(other) != 0 {
		t.Fatalf("missing=%#v err=%v", other, err)
	}

	member := auth.User{ID: "usr_member", Email: "member@example.test", DisplayName: "Member", Role: "member", Status: "active"}
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,created_at,updated_at) VALUES (?,?,?,'hash','member','active',?,?)", member.ID, member.Email, member.DisplayName, now, now); err != nil {
		t.Fatal(err)
	}
	if hidden, _, err := service.ListRequests(ctx, member, UsageQuery{RequestID: admission.RequestID}); err != nil || len(hidden) != 0 {
		t.Fatalf("foreign request leaked: %#v err=%v", hidden, err)
	}
}
