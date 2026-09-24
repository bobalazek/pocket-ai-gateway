package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

type RequestPrice struct {
	ID                     string  `json:"id"`
	Source                 string  `json:"source"`
	InputUSDPerMillion     string  `json:"input_usd_per_million"`
	OutputUSDPerMillion    string  `json:"output_usd_per_million"`
	CacheReadUSDPerMillion *string `json:"cache_read_usd_per_million"`
	WebSearchUSDPerCall    *string `json:"web_search_usd_per_call"`
	EffectiveFrom          string  `json:"effective_from"`
	EffectiveTo            *string `json:"effective_to"`
}

type RequestAttempt struct {
	ID                       string              `json:"id"`
	Ordinal                  int64               `json:"ordinal"`
	ConnectionID             string              `json:"connection_id"`
	ModelID                  string              `json:"model_id"`
	UpstreamID               string              `json:"upstream_model_id"`
	TargetDialect            string              `json:"target_dialect"`
	TargetOperation          string              `json:"target_operation"`
	TranslationApplied       bool                `json:"translation_applied"`
	RequestToolCount         int64               `json:"request_tool_count"`
	WebSearchMaxCalls        *int64              `json:"web_search_max_calls"`
	WebSearchCallCount       *int64              `json:"web_search_call_count"`
	ResponseToolCalls        int64               `json:"response_tool_call_count"`
	ToolCallStatus           string              `json:"tool_call_status"`
	SelectionReason          string              `json:"selection_reason"`
	RejectedCandidates       []map[string]string `json:"rejected_candidates"`
	State                    string              `json:"state"`
	UsageStatus              string              `json:"usage_status"`
	InputTokens              *int64              `json:"input_tokens"`
	OutputTokens             *int64              `json:"output_tokens"`
	CacheCreationInputTokens *int64              `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64              `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    *int64              `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hTokens    *int64              `json:"cache_creation_1h_input_tokens"`
	EstimatedCostUSD         *string             `json:"estimated_cost_usd"`
	AsRecordedCostUSD        *string             `json:"as_recorded_cost_usd"`
	RestatedCostUSD          *string             `json:"restated_cost_usd"`
	CostUSD                  *string             `json:"cost_usd"`
	RecordedPrice            *RequestPrice       `json:"recorded_price"`
	RestatedPrice            *RequestPrice       `json:"restated_price"`
	StartedAt                string              `json:"started_at"`
}

type RequestRecord struct {
	ID          string           `json:"id"`
	OwnerUserID string           `json:"owner_user_id"`
	KeyID       string           `json:"key_id"`
	Operation   string           `json:"operation"`
	Dialect     string           `json:"dialect"`
	ModelID     string           `json:"model_id"`
	State       string           `json:"state"`
	StartedAt   string           `json:"started_at"`
	FinishedAt  *string          `json:"finished_at"`
	Attempts    []RequestAttempt `json:"attempts"`
}

func (service *Service) ListRequests(ctx context.Context, actor auth.User, query UsageQuery) ([]RequestRecord, string, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return nil, "", err
	}
	userID, err := service.visibleUser(ctx, actor, query.UserID)
	if err != nil {
		return nil, "", err
	}
	tx, err := service.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	where, args := "retained_at IS NULL", []any{}
	if query.From != "" || query.To != "" {
		from, to, err := usageRange(service.now(), query.From, query.To)
		if err != nil {
			return nil, "", err
		}
		where += " AND started_at >= ? AND started_at < ?"
		args = append(args, from, to)
	}
	if userID != "" {
		where += " AND owner_user_id=?"
		args = append(args, userID)
	}
	for _, filter := range []struct{ column, value string }{
		{"id", query.RequestID}, {"key_id", query.KeyID}, {"model_id", query.ModelID}, {"dialect", query.Dialect},
		{"operation", query.Operation}, {"state", query.State},
	} {
		if filter.value != "" {
			where += " AND " + filter.column + "=?"
			args = append(args, filter.value)
		}
	}
	if query.ConnectionID != "" {
		where += " AND EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id = requests.id AND attempts.connection_id = ?)"
		args = append(args, query.ConnectionID)
	}
	before, beforeID, err := decodeCursor(query.Cursor)
	if err != nil {
		return nil, "", err
	}
	if query.Cursor != "" {
		where += " AND (started_at<? OR (started_at=? AND id<?))"
		args = append(args, before, before, beforeID)
	}
	args = append(args, 51)
	rows, err := tx.QueryContext(ctx, `SELECT id,owner_user_id,key_id,operation,dialect,model_id,state,started_at,finished_at FROM requests WHERE `+where+` ORDER BY started_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	items := make([]RequestRecord, 0)
	for rows.Next() {
		var item RequestRecord
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.KeyID, &item.Operation, &item.Dialect, &item.ModelID, &item.State, &started, &finished); err != nil {
			rows.Close()
			return nil, "", err
		}
		item.StartedAt = timeString(started)
		item.Attempts = []RequestAttempt{}
		if finished.Valid {
			value := timeString(finished.Int64)
			item.FinishedAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		started, _ := time.Parse(time.RFC3339Nano, items[len(items)-1].StartedAt)
		next = encodeCursor(started.UnixMilli(), items[len(items)-1].ID)
	}
	for index := range items {
		attemptRows, err := tx.QueryContext(ctx, `SELECT
			a.id,a.ordinal,a.connection_id,a.model_id,a.upstream_model_id,a.target_dialect,a.target_operation,a.translation_applied,a.request_tool_count,a.web_search_max_calls,a.web_search_call_count,a.response_tool_call_count,a.tool_call_status,a.selection_reason,a.rejected_candidates_json,a.state,a.usage_status,a.input_tokens,a.output_tokens,a.cache_creation_input_tokens,a.cache_read_input_tokens,a.cache_creation_5m_input_tokens,a.cache_creation_1h_input_tokens,a.estimated_cost_nanos,a.as_recorded_cost_nanos,assessment.amount_nanos,COALESCE(a.restated_cost_nanos,a.as_recorded_cost_nanos),a.started_at,
			recorded.id,recorded.source,recorded.input_nanos_per_million,recorded.output_nanos_per_million,recorded.cache_read_nanos_per_million,recorded.web_search_nanos_per_call,recorded.effective_from,recorded.effective_to,
			restated.id,restated.source,restated.input_nanos_per_million,restated.output_nanos_per_million,restated.cache_read_nanos_per_million,restated.web_search_nanos_per_call,restated.effective_from,restated.effective_to
			FROM attempts a
			LEFT JOIN price_versions recorded ON recorded.id=a.price_version_id
			LEFT JOIN cost_assessments assessment ON assessment.id=(SELECT id FROM cost_assessments WHERE attempt_id=a.id AND kind='restated' ORDER BY created_at DESC,id DESC LIMIT 1)
			LEFT JOIN price_versions restated ON restated.id=assessment.price_version_id
			WHERE a.request_id=? ORDER BY a.ordinal`, items[index].ID)
		if err != nil {
			return nil, "", err
		}
		for attemptRows.Next() {
			var attempt RequestAttempt
			var inputTokens, outputTokens, cacheCreation, cacheRead, cache5m, cache1h sql.NullInt64
			var estimatedCost, recordedCost, restatedCost, effectiveCost sql.NullInt64
			var started int64
			var rejected string
			var webSearchMax, webSearchCount sql.NullInt64
			var recordedID, recordedSource, restatedID, restatedSource sql.NullString
			var recordedInput, recordedOutput, recordedCacheRead, recordedWebSearch, recordedFrom, recordedTo sql.NullInt64
			var restatedInput, restatedOutput, restatedCacheRead, restatedWebSearch, restatedFrom, restatedTo sql.NullInt64
			if err := attemptRows.Scan(&attempt.ID, &attempt.Ordinal, &attempt.ConnectionID, &attempt.ModelID, &attempt.UpstreamID, &attempt.TargetDialect, &attempt.TargetOperation, &attempt.TranslationApplied, &attempt.RequestToolCount, &webSearchMax, &webSearchCount, &attempt.ResponseToolCalls, &attempt.ToolCallStatus, &attempt.SelectionReason, &rejected, &attempt.State, &attempt.UsageStatus, &inputTokens, &outputTokens, &cacheCreation, &cacheRead, &cache5m, &cache1h, &estimatedCost, &recordedCost, &restatedCost, &effectiveCost, &started, &recordedID, &recordedSource, &recordedInput, &recordedOutput, &recordedCacheRead, &recordedWebSearch, &recordedFrom, &recordedTo, &restatedID, &restatedSource, &restatedInput, &restatedOutput, &restatedCacheRead, &restatedWebSearch, &restatedFrom, &restatedTo); err != nil {
				attemptRows.Close()
				return nil, "", err
			}
			_ = json.Unmarshal([]byte(rejected), &attempt.RejectedCandidates)
			if attempt.RejectedCandidates == nil {
				attempt.RejectedCandidates = []map[string]string{}
			}
			attempt.InputTokens = optionalInt64(inputTokens)
			attempt.OutputTokens = optionalInt64(outputTokens)
			attempt.CacheCreationInputTokens = optionalInt64(cacheCreation)
			attempt.CacheReadInputTokens = optionalInt64(cacheRead)
			attempt.CacheCreation5mTokens = optionalInt64(cache5m)
			attempt.CacheCreation1hTokens = optionalInt64(cache1h)
			attempt.WebSearchMaxCalls = optionalInt64(webSearchMax)
			attempt.WebSearchCallCount = optionalInt64(webSearchCount)
			attempt.EstimatedCostUSD = optionalUSD(estimatedCost)
			attempt.AsRecordedCostUSD = optionalUSD(recordedCost)
			attempt.RestatedCostUSD = optionalUSD(restatedCost)
			attempt.CostUSD = optionalUSD(effectiveCost)
			attempt.RecordedPrice = requestPrice(recordedID, recordedSource, recordedInput, recordedOutput, recordedCacheRead, recordedWebSearch, recordedFrom, recordedTo)
			attempt.RestatedPrice = requestPrice(restatedID, restatedSource, restatedInput, restatedOutput, restatedCacheRead, restatedWebSearch, restatedFrom, restatedTo)
			attempt.StartedAt = timeString(started)
			items[index].Attempts = append(items[index].Attempts, attempt)
		}
		if err := attemptRows.Err(); err != nil {
			attemptRows.Close()
			return nil, "", err
		}
		if err := attemptRows.Close(); err != nil {
			return nil, "", err
		}
	}
	return items, next, nil
}

func optionalInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func optionalUSD(value sql.NullInt64) *string {
	if !value.Valid {
		return nil
	}
	formatted := FormatUSD(value.Int64)
	return &formatted
}

func requestPrice(id, source sql.NullString, input, output, cacheRead, webSearch, from, to sql.NullInt64) *RequestPrice {
	if !id.Valid {
		return nil
	}
	price := &RequestPrice{
		ID:                     id.String,
		Source:                 source.String,
		InputUSDPerMillion:     FormatUSD(input.Int64),
		OutputUSDPerMillion:    FormatUSD(output.Int64),
		CacheReadUSDPerMillion: optionalUSD(cacheRead),
		WebSearchUSDPerCall:    optionalUSD(webSearch),
		EffectiveFrom:          timeString(from.Int64),
	}
	if to.Valid {
		value := timeString(to.Int64)
		price.EffectiveTo = &value
	}
	return price
}
