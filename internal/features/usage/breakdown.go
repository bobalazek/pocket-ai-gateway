package usage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

const maxBreakdownOffset = 10_000

type BreakdownRow struct {
	ID                       string `json:"id"`
	Label                    string `json:"label"`
	Requests                 int64  `json:"requests"`
	SuccessfulRequests       int64  `json:"successful_requests"`
	FailedRequests           int64  `json:"failed_requests"`
	Attempts                 int64  `json:"attempts"`
	FailedAttempts           int64  `json:"failed_attempts"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	WebSearchCalls           int64  `json:"web_search_calls"`
	ResponseToolCalls        int64  `json:"response_tool_calls"`
	KnownCostUSD             string `json:"known_cost_usd"`
	UnknownAttempts          int64  `json:"unknown_attempts"`
	FinishedRequests         int64  `json:"finished_requests"`
	AvgGatewayDurationMS     *int64 `json:"avg_gateway_duration_ms"`
	P95GatewayDurationMS     *int64 `json:"p95_gateway_duration_ms"`
}

type Breakdown struct {
	Dimension  string         `json:"dimension"`
	Sort       string         `json:"sort"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Data       []BreakdownRow `json:"data"`
	HasMore    bool           `json:"has_more"`
	NextOffset *int           `json:"next_offset"`
}

// Breakdown groups authoritative records by their own start time: request
// counts/durations by request start, and attempt usage/cost by attempt start.
// Requests are deduplicated across fallback attempts.
func (service *Service) Breakdown(ctx context.Context, actor auth.User, query UsageQuery, dimension, sort string, limit, offset int) (Breakdown, error) {
	requestColumn := map[string]string{
		"user": "r.owner_user_id", "key": "r.key_id", "model": "r.model_id", "connection": "a.connection_id",
		"dialect": "r.dialect", "operation": "r.operation", "state": "r.state",
	}[dimension]
	if requestColumn == "" {
		return Breakdown{}, errors.New("invalid breakdown dimension")
	}
	attemptColumn := requestColumn
	if dimension == "model" {
		attemptColumn = "a.model_id"
	}
	if sort == "" {
		sort = "requests"
	}
	order := map[string]string{
		"requests":    "COALESCE(request_totals.requests, 0) DESC",
		"known_cost":  "COALESCE(attempt_totals.known_cost_nanos, 0) DESC",
		"tokens":      "(COALESCE(attempt_totals.input_tokens, 0) + COALESCE(attempt_totals.output_tokens, 0)) DESC",
		"p95_latency": "(duration_totals.p95_ms IS NULL) ASC, duration_totals.p95_ms DESC",
	}[sort]
	if order == "" {
		return Breakdown{}, errors.New("invalid breakdown sort")
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > maxBreakdownOffset {
		return Breakdown{}, errors.New("breakdown limit or offset is invalid")
	}
	actor, err := refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return Breakdown{}, err
	}
	userID, err := service.visibleUser(ctx, actor, query.UserID)
	if err != nil {
		return Breakdown{}, err
	}
	from, to, err := usageRange(service.now(), query.From, query.To)
	if err != nil {
		return Breakdown{}, err
	}
	requestWhere := []string{"r.started_at >= ?", "r.started_at < ?"}
	attemptWhere := []string{"a.started_at >= ?", "a.started_at < ?"}
	requestArgs := []any{from, to}
	attemptArgs := []any{from, to}
	if userID != "" {
		requestWhere = append(requestWhere, "r.owner_user_id = ?")
		attemptWhere = append(attemptWhere, "r.owner_user_id = ?")
		requestArgs = append(requestArgs, userID)
		attemptArgs = append(attemptArgs, userID)
	}
	if query.KeyID != "" {
		requestWhere = append(requestWhere, "r.key_id = ?")
		attemptWhere = append(attemptWhere, "r.key_id = ?")
		requestArgs = append(requestArgs, query.KeyID)
		attemptArgs = append(attemptArgs, query.KeyID)
	}
	if query.ModelID != "" {
		requestWhere = append(requestWhere, "EXISTS (SELECT 1 FROM attempts ma WHERE ma.request_id = r.id AND ma.model_id = ?)")
		attemptWhere = append(attemptWhere, "a.model_id = ?")
		requestArgs = append(requestArgs, query.ModelID)
		attemptArgs = append(attemptArgs, query.ModelID)
	}
	if query.ConnectionID != "" {
		requestWhere = append(requestWhere, "a.connection_id = ?")
		attemptWhere = append(attemptWhere, "a.connection_id = ?")
		requestArgs = append(requestArgs, query.ConnectionID)
		attemptArgs = append(attemptArgs, query.ConnectionID)
	}
	if dimension == "connection" {
		requestWhere = append(requestWhere, "a.id IS NOT NULL")
	}
	label := "groups.id"
	switch dimension {
	case "user":
		label = `COALESCE((SELECT CASE WHEN TRIM(u.display_name) != '' THEN u.display_name || ' · ' || u.email ELSE u.email END FROM users u WHERE u.id = groups.id), groups.id)`
	case "key":
		label = `COALESCE((SELECT k.label || ' · ' || substr(k.id, 1, 4) || '…' || substr(k.id, -4) FROM api_keys k WHERE k.id = groups.id), groups.id)`
	case "model":
		label = `COALESCE((SELECT m.label FROM public_models m WHERE m.id = groups.id), groups.id)`
	case "connection":
		label = `COALESCE((SELECT c.name FROM provider_connections c WHERE c.id = groups.id), groups.id)`
	case "dialect", "operation":
		label = `CASE WHEN groups.id = '' THEN 'Retained history' ELSE groups.id END`
	}
	statement := `WITH scoped_requests AS (
		SELECT ` + requestColumn + ` AS id, r.id AS request_id, r.state AS request_state,
			r.started_at AS request_started_at, r.finished_at AS request_finished_at
		FROM requests r LEFT JOIN attempts a ON a.request_id = r.id
		WHERE ` + strings.Join(requestWhere, " AND ") + `
	), request_rows AS (
		SELECT id, request_id, MAX(request_state) AS state, MIN(request_started_at) AS started_at, MAX(request_finished_at) AS finished_at
		FROM scoped_requests GROUP BY id, request_id
	), request_totals AS (
		SELECT id, COUNT(*) AS requests, SUM(state = 'succeeded') AS successful_requests, SUM(state = 'failed') AS failed_requests
		FROM request_rows GROUP BY id
	), scoped_attempts AS (
		SELECT ` + attemptColumn + ` AS id, a.id AS attempt_id, a.state AS attempt_state, a.usage_status,
			a.input_tokens, a.output_tokens, a.cache_creation_input_tokens, a.cache_read_input_tokens,
			a.web_search_call_count, a.response_tool_call_count, a.as_recorded_cost_nanos, a.restated_cost_nanos
		FROM attempts a JOIN requests r ON r.id = a.request_id
		WHERE ` + strings.Join(attemptWhere, " AND ") + `
	), attempt_totals AS (
		SELECT id, COUNT(attempt_id) AS attempts, SUM(attempt_state = 'failed') AS failed_attempts,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			COALESCE(SUM(cache_read_input_tokens), 0) AS cache_read_input_tokens,
			COALESCE(SUM(web_search_call_count), 0) AS web_search_calls,
			COALESCE(SUM(response_tool_call_count), 0) AS response_tool_calls,
			COALESCE(SUM(COALESCE(restated_cost_nanos, as_recorded_cost_nanos, 0)), 0) AS known_cost_nanos,
			COALESCE(SUM(CASE WHEN attempt_id IS NOT NULL AND attempt_state != 'cancelled_before_dispatch'
				AND (usage_status = 'unknown' OR COALESCE(restated_cost_nanos, as_recorded_cost_nanos) IS NULL) THEN 1 ELSE 0 END), 0) AS unknown_attempts
		FROM scoped_attempts GROUP BY id
	), duration_ranked AS (
		SELECT id, finished_at - started_at AS duration_ms,
			ROW_NUMBER() OVER (PARTITION BY id ORDER BY finished_at - started_at) AS rank,
			COUNT(*) OVER (PARTITION BY id) AS sample_count
		FROM request_rows WHERE finished_at IS NOT NULL AND finished_at >= started_at
	), duration_totals AS (
		SELECT id, COUNT(*) AS finished_requests, CAST(ROUND(AVG(duration_ms)) AS INTEGER) AS avg_ms,
			MAX(CASE WHEN rank = (sample_count * 95 + 99) / 100 THEN duration_ms END) AS p95_ms
		FROM duration_ranked GROUP BY id
	), groups AS (
		SELECT id FROM request_totals UNION SELECT id FROM attempt_totals
	)
	SELECT groups.id, ` + label + `, COALESCE(request_totals.requests, 0),
		COALESCE(request_totals.successful_requests, 0), COALESCE(request_totals.failed_requests, 0),
		COALESCE(attempt_totals.attempts, 0), COALESCE(attempt_totals.failed_attempts, 0),
		COALESCE(attempt_totals.input_tokens, 0), COALESCE(attempt_totals.output_tokens, 0),
		COALESCE(attempt_totals.cache_creation_input_tokens, 0), COALESCE(attempt_totals.cache_read_input_tokens, 0),
		COALESCE(attempt_totals.web_search_calls, 0), COALESCE(attempt_totals.response_tool_calls, 0),
		COALESCE(attempt_totals.known_cost_nanos, 0), COALESCE(attempt_totals.unknown_attempts, 0),
		COALESCE(duration_totals.finished_requests, 0), duration_totals.avg_ms, duration_totals.p95_ms
	FROM groups LEFT JOIN request_totals ON request_totals.id = groups.id
	LEFT JOIN attempt_totals ON attempt_totals.id = groups.id
	LEFT JOIN duration_totals ON duration_totals.id = groups.id
	ORDER BY ` + order + `, groups.id ASC LIMIT ? OFFSET ?`
	args := append(requestArgs, attemptArgs...)
	args = append(args, limit+1, offset)
	rows, err := service.database.QueryContext(ctx, statement, args...)
	if err != nil {
		return Breakdown{}, err
	}
	defer rows.Close()
	result := Breakdown{Dimension: dimension, Sort: sort, From: timeString(from), To: timeString(to), Data: []BreakdownRow{}}
	for rows.Next() {
		var row BreakdownRow
		var cost int64
		var avg, p95 sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Label, &row.Requests, &row.SuccessfulRequests, &row.FailedRequests,
			&row.Attempts, &row.FailedAttempts, &row.InputTokens, &row.OutputTokens, &row.CacheCreationInputTokens, &row.CacheReadInputTokens,
			&row.WebSearchCalls, &row.ResponseToolCalls, &cost, &row.UnknownAttempts, &row.FinishedRequests, &avg, &p95); err != nil {
			return Breakdown{}, err
		}
		for _, value := range []int64{row.Requests, row.SuccessfulRequests, row.FailedRequests, row.Attempts, row.FailedAttempts, row.InputTokens,
			row.OutputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.WebSearchCalls,
			row.ResponseToolCalls, row.UnknownAttempts, row.FinishedRequests} {
			if value > maxSafeInteger {
				return Breakdown{}, errors.New("breakdown total is too large")
			}
		}
		row.KnownCostUSD = FormatUSD(cost)
		row.AvgGatewayDurationMS = optionalInt64(avg)
		row.P95GatewayDurationMS = optionalInt64(p95)
		result.Data = append(result.Data, row)
	}
	if err := rows.Err(); err != nil {
		return Breakdown{}, err
	}
	if len(result.Data) > limit {
		result.Data = result.Data[:limit]
		result.HasMore = true
		next := offset + limit
		if next <= maxBreakdownOffset {
			result.NextOffset = &next
		}
	}
	return result, nil
}
