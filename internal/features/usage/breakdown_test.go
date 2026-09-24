package usage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

func TestBreakdownDeduplicatesRequestsAndScopesKeys(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return now }
	started := now.Add(-time.Hour).UnixMilli()
	member := auth.User{ID: "usr_member", Email: "member@example.test", DisplayName: "Member", Role: "member", Status: "active"}
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,created_at,updated_at) VALUES (?,?,?,'hash','member','active',?,?)", member.ID, member.Email, member.DisplayName, started, started); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys (id,owner_user_id,label,state,scopes_json,model_patterns_json,connection_ids_json,created_at,updated_at) VALUES ('key_member',?,'Member key','active','[]','[]','[]',?,?)`, member.ID, started, started); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		id, userID, key, state string
		finished               any
	}{
		{"req_one", owner.ID, keyID, "succeeded", started + 1000},
		{"req_two", owner.ID, keyID, "failed", started + 3000},
		{"req_pending", owner.ID, keyID, "reserved", nil},
		{"req_member", member.ID, "key_member", "succeeded", started + 9000},
	} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests (id,owner_user_id,key_id,operation,dialect,model_id,state,started_at,finished_at) VALUES (?,?,?,'chat','openai','model_test',?,?,?)`, request.id, request.userID, request.key, request.state, started, request.finished); err != nil {
			t.Fatal(err)
		}
	}
	for _, attempt := range []struct {
		id, requestID, connection, state, usage string
		ordinal                                 int
		input, output, cost                     any
	}{
		{"att_one", "req_one", "conn_first", "failed", "unknown", 1, nil, nil, nil},
		{"att_two", "req_one", "conn_second", "succeeded", "provider_reported", 2, 10, 5, 200_000_000},
		{"att_three", "req_two", "conn_first", "failed", "provider_reported", 1, 4, 0, 100_000_000},
		{"att_member", "req_member", "conn_first", "succeeded", "provider_reported", 1, 100, 50, 9_000_000_000},
	} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO attempts (id,request_id,ordinal,connection_id,model_id,state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,started_at,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, attempt.id, attempt.requestID, attempt.ordinal, attempt.connection, "model_test", attempt.state, attempt.usage, attempt.input, attempt.output, attempt.cost, started, started+100); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.Breakdown(ctx, owner, UsageQuery{UserID: owner.ID}, "key", "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("owner key breakdown: %#v", result.Data)
	}
	row := result.Data[0]
	if row.ID != keyID || !strings.Contains(row.Label, "Test ·") || row.Requests != 3 || row.SuccessfulRequests != 1 || row.FailedRequests != 1 || row.Attempts != 3 || row.InputTokens != 14 || row.OutputTokens != 5 || row.KnownCostUSD != "0.3" || row.UnknownAttempts != 1 || row.FinishedRequests != 2 || row.AvgGatewayDurationMS == nil || *row.AvgGatewayDurationMS != 2000 || row.P95GatewayDurationMS == nil || *row.P95GatewayDurationMS != 3000 {
		t.Fatalf("incorrect key totals: %#v", row)
	}
	connection, err := service.Breakdown(ctx, owner, UsageQuery{UserID: owner.ID}, "connection", "", 20, 0)
	if err != nil || len(connection.Data) != 2 || connection.Data[0].ID != "conn_first" || connection.Data[0].Requests != 2 || connection.Data[0].FailedAttempts != 2 || connection.Data[0].UnknownAttempts != 1 {
		t.Fatalf("connection breakdown: %#v, %v", connection.Data, err)
	}
	memberResult, err := service.Breakdown(ctx, member, UsageQuery{}, "key", "", 20, 0)
	if err != nil || len(memberResult.Data) != 1 || memberResult.Data[0].ID != "key_member" {
		t.Fatalf("member visibility: %#v, %v", memberResult.Data, err)
	}
	if _, err := service.Breakdown(ctx, member, UsageQuery{UserID: owner.ID}, "key", "", 20, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("member requested owner usage: %v", err)
	}
	users, err := service.Breakdown(ctx, owner, UsageQuery{}, "user", "", 20, 0)
	if err != nil || len(users.Data) != 2 || users.Data[0].ID != owner.ID || users.Data[1].ID != member.ID || users.Data[1].Label != "Member · member@example.test" {
		t.Fatalf("owner user breakdown: %#v, %v", users.Data, err)
	}
	self, err := service.Breakdown(ctx, member, UsageQuery{}, "user", "", 20, 0)
	if err != nil || len(self.Data) != 1 || self.Data[0].ID != member.ID || self.Data[0].Label != "Member · member@example.test" {
		t.Fatalf("member user breakdown: %#v, %v", self.Data, err)
	}
	if _, err := service.Breakdown(ctx, owner, UsageQuery{}, "invalid", "", 20, 0); err == nil {
		t.Fatal("invalid dimension accepted")
	}
	if _, err := service.Breakdown(ctx, owner, UsageQuery{}, "key", "", 20, 10_001); err == nil {
		t.Fatal("unbounded offset accepted")
	}
	for _, sort := range []string{"known_cost", "tokens", "p95_latency"} {
		ranked, err := service.Breakdown(ctx, owner, UsageQuery{}, "key", sort, 1, 0)
		if err != nil || len(ranked.Data) != 1 || ranked.Data[0].ID != "key_member" || !ranked.HasMore || ranked.Sort != sort {
			t.Fatalf("%s ranking: %#v, %v", sort, ranked, err)
		}
	}
	if _, err := service.Breakdown(ctx, owner, UsageQuery{}, "key", "unbounded", 20, 0); err == nil {
		t.Fatal("invalid sort accepted")
	}
	if _, err := store.SystemDB().ExecContext(ctx, "UPDATE requests SET operation='', dialect='', retained_at=? WHERE id='req_pending'", started); err != nil {
		t.Fatal(err)
	}
	operations, err := service.Breakdown(ctx, owner, UsageQuery{UserID: owner.ID}, "operation", "", 20, 0)
	if err != nil || len(operations.Data) != 2 || operations.Data[1].ID != "" || operations.Data[1].Label != "Retained history" {
		t.Fatalf("retained operation bucket: %#v, %v", operations.Data, err)
	}
}

func TestBreakdownAttributesRequestsAndAttemptsToTheirOwnStartTimes(t *testing.T) {
	ctx, service, owner, keyID, store := testService(t)
	defer store.Close()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	from := now.Add(-time.Hour)
	for _, item := range []struct {
		id, state         string
		started, finished int64
	}{
		{"req_cross", "succeeded", from.Add(-time.Second).UnixMilli(), from.Add(3 * time.Second).UnixMilli()},
		{"req_inside", "succeeded", now.Add(-2 * time.Second).UnixMilli(), now.Add(time.Second).UnixMilli()},
	} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests (id,owner_user_id,key_id,operation,dialect,model_id,state,started_at,finished_at) VALUES (?,?,?,'chat','openai','model_test',?,?,?)`, item.id, owner.ID, keyID, item.state, item.started, item.finished); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id, request, connection, model, state, usage string
		ordinal                                      int
		started, input, cost                         int64
	}{
		{"att_failed", "req_cross", "conn_first", "model_test", "failed", "unknown", 1, from.Add(-time.Second).UnixMilli(), 0, 0},
		{"att_retry", "req_cross", "conn_second", "model_alt", "succeeded", "provider_reported", 2, from.Add(time.Second).UnixMilli(), 7, 200_000_000},
		{"att_after", "req_inside", "conn_first", "model_test", "succeeded", "provider_reported", 1, now.Add(time.Second).UnixMilli(), 11, 300_000_000},
	} {
		var input, cost any
		if item.usage == "provider_reported" {
			input, cost = item.input, item.cost
		}
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO attempts (id,request_id,ordinal,connection_id,model_id,state,usage_status,input_tokens,output_tokens,as_recorded_cost_nanos,started_at) VALUES (?,?,?,?,?,?,?,?,0,?,?)`, item.id, item.request, item.ordinal, item.connection, item.model, item.state, item.usage, input, cost, item.started); err != nil {
			t.Fatal(err)
		}
	}
	query := UsageQuery{UserID: owner.ID, From: from.Format(time.RFC3339), To: now.Format(time.RFC3339)}
	summary, err := service.Summary(ctx, owner, query)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Breakdown(ctx, owner, query, "key", "", 20, 0)
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("key breakdown = %#v, %v", result.Data, err)
	}
	row := result.Data[0]
	if summary.Requests != 1 || summary.Attempts != 1 || summary.InputTokens != 7 || summary.KnownCostUSD != "0.2" ||
		row.Requests != summary.Requests || row.Attempts != summary.Attempts || row.InputTokens != summary.InputTokens || row.KnownCostUSD != summary.KnownCostUSD {
		t.Fatalf("period attribution differs: summary=%#v breakdown=%#v", summary, row)
	}
	modelQuery := query
	modelQuery.ModelID = "model_alt"
	modelSummary, err := service.Summary(ctx, owner, modelQuery)
	if err != nil {
		t.Fatal(err)
	}
	modelBreakdown, err := service.Breakdown(ctx, owner, modelQuery, "model", "", 20, 0)
	if err != nil || len(modelBreakdown.Data) != 1 || modelSummary.Requests != 0 || modelSummary.InputTokens != 7 || modelBreakdown.Data[0].Requests != modelSummary.Requests || modelBreakdown.Data[0].InputTokens != modelSummary.InputTokens {
		t.Fatalf("model period attribution differs: summary=%#v breakdown=%#v, %v", modelSummary, modelBreakdown, err)
	}
	windowProviders, err := service.Breakdown(ctx, owner, query, "connection", "", 20, 0)
	if err != nil || len(windowProviders.Data) != 2 || windowProviders.Data[0].ID != "conn_first" || windowProviders.Data[0].Requests != 1 || windowProviders.Data[0].Attempts != 0 || windowProviders.Data[1].ID != "conn_second" || windowProviders.Data[1].Requests != 0 || windowProviders.Data[1].Attempts != 1 || windowProviders.Data[1].KnownCostUSD != "0.2" {
		t.Fatalf("provider period attribution = %#v, %v", windowProviders.Data, err)
	}
	all := UsageQuery{UserID: owner.ID, From: from.Add(-time.Hour).Format(time.RFC3339), To: now.Add(time.Hour).Format(time.RFC3339)}
	providers, err := service.Breakdown(ctx, owner, all, "connection", "", 20, 0)
	if err != nil || len(providers.Data) != 2 {
		t.Fatalf("provider breakdown = %#v, %v", providers.Data, err)
	}
	first := providers.Data[0]
	if first.ID != "conn_first" || first.FailedAttempts != 1 || first.FailedRequests != 0 || first.SuccessfulRequests != 2 {
		t.Fatalf("failed fallback attempt concealed by successful request: %#v", first)
	}
}

func TestBreakdownPagesWithoutClaimingGlobalTotals(t *testing.T) {
	ctx, service, owner, _, store := testService(t)
	defer store.Close()
	now := time.Now().Add(-time.Second).UnixMilli()
	for _, item := range []struct{ key, request string }{{"key_more_a", "req_more_a"}, {"key_more_b", "req_more_b"}} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys (id,owner_user_id,label,state,scopes_json,model_patterns_json,connection_ids_json,created_at,updated_at) VALUES (?,?,'Extra','active','[]','[]','[]',?,?)`, item.key, owner.ID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests (id,owner_user_id,key_id,operation,dialect,model_id,state,started_at) VALUES (?,?,?,'chat','openai','model_test','reserved',?)`, item.request, owner.ID, item.key, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.Breakdown(ctx, owner, UsageQuery{}, "key", "", 1, 0)
	if err != nil || len(first.Data) != 1 || !first.HasMore || first.NextOffset == nil || *first.NextOffset != 1 || first.Data[0].Attempts != 0 || first.Data[0].AvgGatewayDurationMS != nil {
		t.Fatalf("first page: %#v, %v", first, err)
	}
	second, err := service.Breakdown(ctx, owner, UsageQuery{}, "key", "", 1, *first.NextOffset)
	if err != nil || len(second.Data) != 1 || second.HasMore || second.NextOffset != nil || second.Data[0].ID == first.Data[0].ID {
		t.Fatalf("second page: %#v, %v", second, err)
	}
}
