package usage

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

type UsagePoint struct {
	Date                     string `json:"date"`
	Requests                 int64  `json:"requests"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    int64  `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hTokens    int64  `json:"cache_creation_1h_input_tokens"`
	WebSearchCalls           int64  `json:"web_search_calls"`
	KnownCostUSD             string `json:"known_cost_usd"`
	UnknownAttempts          int64  `json:"unknown_attempts"`
}

type UsageSummary struct {
	From                     string       `json:"from"`
	To                       string       `json:"to"`
	Requests                 int64        `json:"requests"`
	Attempts                 int64        `json:"attempts"`
	InputTokens              int64        `json:"input_tokens"`
	OutputTokens             int64        `json:"output_tokens"`
	CacheCreationInputTokens int64        `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64        `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    int64        `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hTokens    int64        `json:"cache_creation_1h_input_tokens"`
	WebSearchCalls           int64        `json:"web_search_calls"`
	KnownCostUSD             string       `json:"known_cost_usd"`
	EstimatedCostUSD         string       `json:"estimated_cost_usd"`
	AsRecordedCostUSD        string       `json:"as_recorded_cost_usd"`
	RestatementDeltaUSD      string       `json:"restatement_delta_usd"`
	UnknownAttempts          int64        `json:"unknown_attempts"`
	Points                   []UsagePoint `json:"points"`
}

type UnresolvedAttempt struct {
	ID            string `json:"id"`
	RequestID     string `json:"request_id"`
	OwnerUserID   string `json:"owner_user_id"`
	KeyID         string `json:"key_id"`
	ModelID       string `json:"model_id"`
	ConnectionID  string `json:"connection_id"`
	State         string `json:"state"`
	UsageStatus   string `json:"usage_status"`
	EstimatedCost string `json:"estimated_cost_usd,omitempty"`
	StartedAt     string `json:"started_at"`
}

type UsageQuery struct {
	UserID       string
	From         string
	To           string
	KeyID        string
	ModelID      string
	ConnectionID string
	Dialect      string
	Operation    string
	State        string
	Cursor       string
	RequestID    string
}

func (service *Service) Summary(ctx context.Context, actor auth.User, query UsageQuery) (UsageSummary, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return UsageSummary{}, err
	}
	userID, err := service.visibleUser(ctx, actor, query.UserID)
	if err != nil {
		return UsageSummary{}, err
	}
	from, to, err := usageRange(service.now(), query.From, query.To)
	if err != nil {
		return UsageSummary{}, err
	}
	tx, err := service.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsageSummary{}, err
	}
	defer tx.Rollback()
	filter, args := usageFilter(userID, from, to, query)
	var summary UsageSummary
	var estimatedCost, recordedCost, currentCost int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(attempts.id), COALESCE(SUM(attempts.input_tokens), 0), COALESCE(SUM(attempts.output_tokens), 0), COALESCE(SUM(attempts.cache_creation_input_tokens), 0), COALESCE(SUM(attempts.cache_read_input_tokens), 0), COALESCE(SUM(attempts.cache_creation_5m_input_tokens), 0), COALESCE(SUM(attempts.cache_creation_1h_input_tokens), 0), COALESCE(SUM(attempts.web_search_call_count), 0),
		COALESCE(SUM(attempts.estimated_cost_nanos), 0), COALESCE(SUM(attempts.as_recorded_cost_nanos), 0),
		COALESCE(SUM(COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos, 0)), 0),
		COALESCE(SUM(CASE WHEN attempts.state != 'cancelled_before_dispatch' AND (attempts.usage_status = 'unknown' OR COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos) IS NULL) THEN 1 ELSE 0 END), 0)
		FROM attempts JOIN requests ON requests.id = attempts.request_id WHERE `+filter, args...).Scan(&summary.Attempts, &summary.InputTokens, &summary.OutputTokens, &summary.CacheCreationInputTokens, &summary.CacheReadInputTokens, &summary.CacheCreation5mTokens, &summary.CacheCreation1hTokens, &summary.WebSearchCalls, &estimatedCost, &recordedCost, &currentCost, &summary.UnknownAttempts)
	if err != nil {
		return UsageSummary{}, err
	}
	requestWhere, requestArgs := requestUsageFilter(userID, from, to, query)
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests WHERE "+requestWhere, requestArgs...).Scan(&summary.Requests); err != nil {
		return UsageSummary{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', attempts.started_at / 1000, 'unixepoch'), COUNT(DISTINCT requests.id), COALESCE(SUM(attempts.input_tokens), 0), COALESCE(SUM(attempts.output_tokens), 0), COALESCE(SUM(attempts.cache_creation_input_tokens), 0), COALESCE(SUM(attempts.cache_read_input_tokens), 0), COALESCE(SUM(attempts.cache_creation_5m_input_tokens), 0), COALESCE(SUM(attempts.cache_creation_1h_input_tokens), 0), COALESCE(SUM(attempts.web_search_call_count), 0),
		COALESCE(SUM(COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos, 0)), 0), COALESCE(SUM(CASE WHEN attempts.state != 'cancelled_before_dispatch' AND (attempts.usage_status = 'unknown' OR COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos) IS NULL) THEN 1 ELSE 0 END), 0)
		FROM attempts JOIN requests ON requests.id = attempts.request_id WHERE `+filter+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return UsageSummary{}, err
	}
	points := map[string]UsagePoint{}
	for rows.Next() {
		var point UsagePoint
		var pointCost int64
		var ignoredAttemptRequests int64
		if err := rows.Scan(&point.Date, &ignoredAttemptRequests, &point.InputTokens, &point.OutputTokens, &point.CacheCreationInputTokens, &point.CacheReadInputTokens, &point.CacheCreation5mTokens, &point.CacheCreation1hTokens, &point.WebSearchCalls, &pointCost, &point.UnknownAttempts); err != nil {
			rows.Close()
			return UsageSummary{}, err
		}
		point.KnownCostUSD = FormatUSD(pointCost)
		points[point.Date] = point
	}
	if err := rows.Close(); err != nil {
		return UsageSummary{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', requests.started_at / 1000, 'unixepoch'), COUNT(*) FROM requests WHERE `+requestWhere+` GROUP BY 1`, requestArgs...)
	if err != nil {
		return UsageSummary{}, err
	}
	for rows.Next() {
		var date string
		var count int64
		if err := rows.Scan(&date, &count); err != nil {
			rows.Close()
			return UsageSummary{}, err
		}
		point := points[date]
		point.Date, point.Requests = date, count
		if point.KnownCostUSD == "" {
			point.KnownCostUSD = "0"
		}
		points[date] = point
	}
	if err := rows.Close(); err != nil {
		return UsageSummary{}, err
	}
	dates := make([]string, 0, len(points))
	for date := range points {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	summary.Points = make([]UsagePoint, 0, len(dates))
	for _, date := range dates {
		point := points[date]
		if point.Requests > maxSafeInteger || point.InputTokens > maxSafeInteger || point.OutputTokens > maxSafeInteger || point.CacheCreationInputTokens > maxSafeInteger || point.CacheReadInputTokens > maxSafeInteger || point.CacheCreation5mTokens > maxSafeInteger || point.CacheCreation1hTokens > maxSafeInteger || point.WebSearchCalls > maxSafeInteger || point.UnknownAttempts > maxSafeInteger {
			return UsageSummary{}, errors.New("usage totals exceed the dashboard-safe integer range")
		}
		summary.Points = append(summary.Points, point)
	}
	if summary.Requests > maxSafeInteger || summary.Attempts > maxSafeInteger || summary.InputTokens > maxSafeInteger || summary.OutputTokens > maxSafeInteger || summary.CacheCreationInputTokens > maxSafeInteger || summary.CacheReadInputTokens > maxSafeInteger || summary.CacheCreation5mTokens > maxSafeInteger || summary.CacheCreation1hTokens > maxSafeInteger || summary.WebSearchCalls > maxSafeInteger || summary.UnknownAttempts > maxSafeInteger {
		return UsageSummary{}, errors.New("usage totals exceed the dashboard-safe integer range")
	}
	delta, ok := checkedAdd(currentCost, -recordedCost)
	if !ok {
		return UsageSummary{}, errors.New("usage cost total is too large")
	}
	summary.From, summary.To = timeString(from), timeString(to)
	summary.KnownCostUSD, summary.EstimatedCostUSD, summary.AsRecordedCostUSD, summary.RestatementDeltaUSD = FormatUSD(currentCost), FormatUSD(estimatedCost), FormatUSD(recordedCost), FormatUSD(delta)
	if err := tx.Commit(); err != nil {
		return UsageSummary{}, err
	}
	return summary, nil
}

func (service *Service) Unresolved(ctx context.Context, actor auth.User, query UsageQuery) ([]UnresolvedAttempt, string, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return nil, "", err
	}
	userID, err := service.visibleUser(ctx, actor, query.UserID)
	if err != nil {
		return nil, "", err
	}
	where := "(attempts.usage_status = 'unknown' OR reservations.state = 'uncertain' OR (attempts.state != 'cancelled_before_dispatch' AND COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos) IS NULL))"
	args := []any{}
	if query.From != "" || query.To != "" {
		from, to, err := usageRange(service.now(), query.From, query.To)
		if err != nil {
			return nil, "", err
		}
		where += " AND attempts.started_at >= ? AND attempts.started_at < ?"
		args = append(args, from, to)
	}
	if userID != "" {
		where += " AND requests.owner_user_id = ?"
		args = append(args, userID)
	}
	for _, item := range []struct{ column, value string }{{"requests.key_id", query.KeyID}, {"attempts.model_id", query.ModelID}, {"attempts.connection_id", query.ConnectionID}} {
		if item.value != "" {
			where += " AND " + item.column + " = ?"
			args = append(args, item.value)
		}
	}
	before, beforeID, err := decodeCursor(query.Cursor)
	if err != nil {
		return nil, "", err
	}
	if query.Cursor != "" {
		where += " AND (attempts.started_at < ? OR (attempts.started_at = ? AND attempts.id < ?))"
		args = append(args, before, before, beforeID)
	}
	rows, err := service.database.QueryContext(ctx, `SELECT DISTINCT attempts.id, attempts.request_id, requests.owner_user_id, requests.key_id, attempts.model_id, attempts.connection_id, attempts.state, attempts.usage_status, attempts.estimated_cost_nanos, attempts.started_at
		FROM attempts JOIN requests ON requests.id = attempts.request_id LEFT JOIN reservations ON reservations.attempt_id = attempts.id WHERE `+where+` ORDER BY attempts.started_at DESC, attempts.id DESC LIMIT 51`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []UnresolvedAttempt{}
	for rows.Next() {
		var item UnresolvedAttempt
		var estimate sql.NullInt64
		var started int64
		if err := rows.Scan(&item.ID, &item.RequestID, &item.OwnerUserID, &item.KeyID, &item.ModelID, &item.ConnectionID, &item.State, &item.UsageStatus, &estimate, &started); err != nil {
			return nil, "", err
		}
		if estimate.Valid {
			item.EstimatedCost = FormatUSD(estimate.Int64)
		}
		item.StartedAt = timeString(started)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) <= 50 {
		return items, "", nil
	}
	items = items[:50]
	started, _ := time.Parse(time.RFC3339Nano, items[len(items)-1].StartedAt)
	return items, encodeCursor(started.UnixMilli(), items[len(items)-1].ID), nil
}

func (service *Service) visibleUser(ctx context.Context, actor auth.User, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if actor.Role == "member" {
		if requested != "" && requested != actor.ID {
			return "", ErrDenied
		}
		return actor.ID, nil
	}
	if requested == "" {
		if actor.Role == "owner" {
			return "", nil
		}
		return actor.ID, nil
	}
	if actor.Role == "owner" {
		return requested, nil
	}
	var role string
	err := service.database.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", requested).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if role != "member" {
		return "", ErrDenied
	}
	return requested, nil
}

func usageRange(now time.Time, fromValue, toValue string) (int64, int64, error) {
	to := now.UTC()
	from := to.AddDate(0, 0, -30)
	var err error
	if fromValue != "" {
		from, err = time.Parse(time.RFC3339, fromValue)
		if err != nil {
			return 0, 0, errors.New("from must be RFC3339")
		}
	}
	if toValue != "" {
		to, err = time.Parse(time.RFC3339, toValue)
		if err != nil {
			return 0, 0, errors.New("to must be RFC3339")
		}
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return 0, 0, errors.New("usage range must be positive and at most 366 days")
	}
	return from.UnixMilli(), to.UnixMilli(), nil
}

func usageFilter(userID string, from, to int64, query UsageQuery) (string, []any) {
	filter := "attempts.started_at >= ? AND attempts.started_at < ?"
	args := []any{from, to}
	if userID != "" {
		filter += " AND requests.owner_user_id = ?"
		args = append(args, userID)
	}
	for _, item := range []struct {
		column, value string
	}{{"requests.key_id", query.KeyID}, {"attempts.model_id", query.ModelID}, {"attempts.connection_id", query.ConnectionID}} {
		if item.value != "" {
			filter += " AND " + item.column + " = ?"
			args = append(args, item.value)
		}
	}
	return filter, args
}

func requestUsageFilter(userID string, from, to int64, query UsageQuery) (string, []any) {
	filter := "requests.started_at >= ? AND requests.started_at < ?"
	args := []any{from, to}
	if userID != "" {
		filter += " AND requests.owner_user_id = ?"
		args = append(args, userID)
	}
	if query.KeyID != "" {
		filter += " AND requests.key_id = ?"
		args = append(args, query.KeyID)
	}
	if query.ModelID != "" {
		filter += " AND EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id = requests.id AND attempts.model_id = ?)"
		args = append(args, query.ModelID)
	}
	if query.ConnectionID != "" {
		filter += " AND EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id = requests.id AND attempts.connection_id = ?)"
		args = append(args, query.ConnectionID)
	}
	return filter, args
}

type EffectiveLimit struct {
	PolicyID       string  `json:"policy_id"`
	ScopeKind      string  `json:"scope_kind"`
	Metric         string  `json:"metric"`
	Algorithm      string  `json:"algorithm"`
	Period         string  `json:"period"`
	LimitUnits     int64   `json:"limit_units"`
	LimitUSD       string  `json:"limit_usd,omitempty"`
	ConsumedUSD    string  `json:"consumed_usd,omitempty"`
	ConsumedUnits  int64   `json:"consumed_units"`
	ReservedUSD    string  `json:"reserved_usd,omitempty"`
	ReservedUnits  int64   `json:"reserved_units"`
	RemainingUnits int64   `json:"remaining_units"`
	RemainingUSD   string  `json:"remaining_usd,omitempty"`
	ResetsAt       *string `json:"resets_at"`
}

func (service *Service) EffectiveLimits(ctx context.Context, actor auth.User, keyID, connectionID string) ([]EffectiveLimit, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return nil, err
	}
	var ownerID string
	if err := service.database.QueryRowContext(ctx, "SELECT owner_user_id FROM api_keys WHERE id = ?", keyID).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if actor.Role == "member" && ownerID != actor.ID {
		return nil, ErrNotFound
	}
	if actor.Role == "admin" && ownerID != actor.ID {
		var role string
		if err := service.database.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", ownerID).Scan(&role); err != nil {
			return nil, err
		}
		if role != "member" {
			return nil, ErrDenied
		}
	}
	effective, err := service.effectiveTime(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := service.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	policies, err := matchingPolicies(ctx, tx, ownerID, keyID, connectionID)
	if err != nil {
		return nil, err
	}
	items := make([]EffectiveLimit, 0, len(policies))
	for _, policy := range policies {
		item := EffectiveLimit{PolicyID: policy.ID, ScopeKind: policy.ScopeKind, Metric: policy.Metric, Algorithm: policy.Algorithm, Period: policy.Period, LimitUnits: policy.Limit, RemainingUnits: policy.Limit}
		if policy.Algorithm == "ceiling" {
			// Per-request ceilings have no durable consumption counter.
		} else if policy.Algorithm == "concurrency" {
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM concurrency_leases WHERE policy_id = ?", policy.ID).Scan(&item.ConsumedUnits); err != nil {
				return nil, err
			}
			item.RemainingUnits = remainingCapacity(policy.Limit, item.ConsumedUnits, 0)
		} else if policy.Algorithm == "token_bucket" {
			var remaining, remainder, lastRefill, lastEffective int64
			err := tx.QueryRowContext(ctx, "SELECT remaining_units, refill_remainder, last_refill_at, last_effective_at FROM bucket_state WHERE policy_id = ?", policy.ID).Scan(&remaining, &remainder, &lastRefill, &lastEffective)
			if err == nil {
				refill, _, _ := refillAmount(max(effective, lastEffective)-lastRefill, policy.Refill, policy.RefillInterval, remainder)
				item.RemainingUnits = min(policy.Limit, saturatingAdd(remaining, refill))
				item.ConsumedUnits = remainingCapacity(policy.Limit, item.RemainingUnits, 0)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		} else {
			start, end := periodBounds(policy, effective)
			err := tx.QueryRowContext(ctx, "SELECT consumed_units, reserved_units FROM quota_periods WHERE policy_id = ? AND period_start = ?", policy.ID, start).Scan(&item.ConsumedUnits, &item.ReservedUnits)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			item.RemainingUnits = remainingCapacity(policy.Limit, item.ConsumedUnits, item.ReservedUnits)
			if end != nil {
				value := timeString(*end)
				item.ResetsAt = &value
			}
		}
		if policy.Metric == "spend" {
			item.LimitUSD, item.ConsumedUSD, item.ReservedUSD, item.RemainingUSD = FormatUSD(policy.Limit), FormatUSD(item.ConsumedUnits), FormatUSD(item.ReservedUnits), FormatUSD(item.RemainingUnits)
		}
		if item.LimitUnits > maxSafeInteger || item.ConsumedUnits > maxSafeInteger || item.ReservedUnits > maxSafeInteger || item.RemainingUnits > maxSafeInteger {
			return nil, errors.New("limit counters exceed the dashboard-safe integer range")
		}
		items = append(items, item)
	}
	return items, tx.Commit()
}

func remainingCapacity(limit, consumed, reserved int64) int64 {
	value, ok := checkedAdd(limit, -consumed, -reserved)
	if !ok || value < 0 {
		return 0
	}
	return value
}
