package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

type OutboxStatus struct {
	PendingEvents  int64   `json:"pending_events"`
	ReservedEvents int64   `json:"reserved_events"`
	PendingBytes   int64   `json:"pending_bytes"`
	OldestEventAt  *string `json:"oldest_event_at"`
	Full           bool    `json:"full"`
}

func OutboxState(ctx context.Context, system *sql.DB) (OutboxStatus, error) {
	var status OutboxStatus
	var oldest sql.NullInt64
	err := system.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM event_outbox WHERE delivered_at IS NULL),
		(SELECT COUNT(*) FROM attempts WHERE state IN ('reserved', 'dispatching', 'streaming')),
		(SELECT COALESCE(SUM(length(payload_json)), 0) FROM event_outbox WHERE delivered_at IS NULL),
		(SELECT MIN(created_at) FROM event_outbox WHERE delivered_at IS NULL)`).Scan(&status.PendingEvents, &status.ReservedEvents, &status.PendingBytes, &oldest)
	if err != nil {
		return status, err
	}
	if oldest.Valid {
		value := timeString(oldest.Int64)
		status.OldestEventAt = &value
	}
	status.Full = status.PendingEvents+status.ReservedEvents >= maxPendingOutboxEvents || status.PendingBytes+status.ReservedEvents*outboxEventAllowance >= maxPendingOutboxBytes
	return status, nil
}

func (service *Service) OutboxStatus(ctx context.Context, actor auth.User) (OutboxStatus, error) {
	actor, err := refreshUsageActor(ctx, service.database, actor)
	if err != nil || actor.Role != "owner" && actor.Role != "admin" {
		return OutboxStatus{}, ErrDenied
	}
	return OutboxState(ctx, service.database)
}

func ProjectOutbox(ctx context.Context, store *storage.Store, limit int) (int, error) {
	unlock := store.LockProjection()
	defer unlock()
	system, data := store.SystemDB(), store.DataDB()
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var cutoff int64
	var cutoffValue string
	if err := data.QueryRowContext(ctx, "SELECT value FROM projection_metadata WHERE key='event_detail_cutoff'").Scan(&cutoffValue); err == nil {
		cutoff, _ = strconv.ParseInt(cutoffValue, 10, 64)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	rows, err := system.QueryContext(ctx, "SELECT id, event_type, request_id, attempt_id, payload_json, created_at FROM event_outbox WHERE delivered_at IS NULL ORDER BY sequence LIMIT ?", limit)
	if err != nil {
		return 0, err
	}
	type event struct {
		id, eventType, payload string
		requestID, attemptID   sql.NullString
		createdAt              int64
	}
	var events []event
	for rows.Next() {
		var item event
		if err := rows.Scan(&item.id, &item.eventType, &item.requestID, &item.attemptID, &item.payload, &item.createdAt); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	delivered := 0
	for _, item := range events {
		tx, err := data.BeginTx(ctx, nil)
		if err != nil {
			return delivered, err
		}
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO usage_events (event_id, event_type, request_id, attempt_id, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)", item.id, item.eventType, item.requestID, item.attemptID, item.payload, item.createdAt)
		if err != nil {
			tx.Rollback()
			return delivered, err
		}
		inserted, _ := result.RowsAffected()
		if inserted == 1 && item.eventType == "attempt.settled" {
			requestIncrement := int64(0)
			if item.requestID.Valid {
				var settlementEvents int64
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events WHERE event_type = 'attempt.settled' AND request_id = ?", item.requestID.String).Scan(&settlementEvents); err != nil {
					tx.Rollback()
					return delivered, err
				}
				if settlementEvents == 1 {
					requestIncrement = 1
				}
			}
			if err := projectSettlement(ctx, tx, item.payload, requestIncrement); err != nil {
				tx.Rollback()
				return delivered, err
			}
		} else if inserted == 1 && item.eventType == "request.cancelled_before_dispatch" {
			requestIncrement := int64(0)
			if item.requestID.Valid {
				var count int64
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events WHERE request_id = ? AND event_type IN ('attempt.settled', 'request.cancelled_before_dispatch')", item.requestID.String).Scan(&count); err != nil {
					tx.Rollback()
					return delivered, err
				}
				if count == 1 {
					requestIncrement = 1
				}
			}
			if err := projectCancelledRequest(ctx, tx, item.payload, requestIncrement); err != nil {
				tx.Rollback()
				return delivered, err
			}
		} else if inserted == 1 && item.eventType == "attempt.reconciled" {
			if err := projectReconciliation(ctx, tx, item.payload); err != nil {
				tx.Rollback()
				return delivered, err
			}
		} else if inserted == 1 && item.eventType == "attempt.cost_adjusted" {
			if err := projectCostAdjustment(ctx, tx, item.payload); err != nil {
				tx.Rollback()
				return delivered, err
			}
		}
		if inserted == 1 && cutoff > 0 && item.createdAt < cutoff {
			if _, err := tx.ExecContext(ctx, "UPDATE usage_events SET payload_json='{}' WHERE event_id=?", item.id); err != nil {
				tx.Rollback()
				return delivered, err
			}
		}
		if err := tx.Commit(); err != nil {
			return delivered, err
		}
		if _, err := system.ExecContext(ctx, "UPDATE event_outbox SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL", time.Now().UnixMilli(), item.id); err != nil {
			return delivered, err
		}
		if _, err := system.ExecContext(ctx, "DELETE FROM event_outbox WHERE id = ? AND delivered_at IS NOT NULL", item.id); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func projectReconciliation(ctx context.Context, tx *sql.Tx, payload string) error {
	var item struct {
		OwnerUserID  string `json:"owner_user_id"`
		KeyID        string `json:"key_id"`
		ModelID      string `json:"model_id"`
		ConnectionID string `json:"connection_id"`
		StartedAt    int64  `json:"started_at"`
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		CostNanos    *int64 `json:"cost_nanos"`
		UnknownDelta int64  `json:"unknown_delta"`
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return err
	}
	// The original interrupted settlement already created this row with unknown_attempts = 1.
	input, output, cost := int64(0), int64(0), int64(0)
	if item.InputTokens != nil {
		input = *item.InputTokens
	}
	if item.OutputTokens != nil {
		output = *item.OutputTokens
	}
	if item.CostNanos != nil {
		cost = *item.CostNanos
	}
	date := time.UnixMilli(item.StartedAt).UTC().Format("2006-01-02")
	var currentInput, currentOutput, currentCost, currentUnknown int64
	if err := tx.QueryRowContext(ctx, `SELECT input_tokens, output_tokens, known_cost_nanos, unknown_attempts FROM usage_daily WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID).Scan(&currentInput, &currentOutput, &currentCost, &currentUnknown); err != nil {
		return errors.New("reconciliation has no projected interrupted attempt")
	}
	nextInput, okInput := checkedAdd(currentInput, input)
	nextOutput, okOutput := checkedAdd(currentOutput, output)
	nextCost, okCost := checkedAdd(currentCost, cost)
	nextUnknown, okUnknown := checkedAdd(currentUnknown, item.UnknownDelta)
	if !okInput || !okOutput || !okCost || !okUnknown || nextUnknown < 0 {
		return errors.New("reconciliation projection overflow")
	}
	_, err := tx.ExecContext(ctx, `UPDATE usage_daily SET input_tokens = ?, output_tokens = ?, known_cost_nanos = ?, unknown_attempts = ? WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`, nextInput, nextOutput, nextCost, nextUnknown, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID)
	return err
}

func projectCancelledRequest(ctx context.Context, tx *sql.Tx, payload string, requestIncrement int64) error {
	var item struct {
		OwnerUserID  string `json:"owner_user_id"`
		KeyID        string `json:"key_id"`
		ModelID      string `json:"model_id"`
		ConnectionID string `json:"connection_id"`
		StartedAt    int64  `json:"started_at"`
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return err
	}
	if item.OwnerUserID == "" || item.KeyID == "" || item.ModelID == "" || item.ConnectionID == "" || item.StartedAt == 0 {
		return errors.New("invalid cancelled request projection")
	}
	date := time.UnixMilli(item.StartedAt).UTC().Format("2006-01-02")
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_daily (date, owner_user_id, key_id, model_id, connection_id, requests)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(date, owner_user_id, key_id, model_id, connection_id) DO UPDATE SET requests = requests + excluded.requests`,
		date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID, requestIncrement)
	return err
}

func projectCostAdjustment(ctx context.Context, tx *sql.Tx, payload string) error {
	var item struct {
		OwnerUserID  string `json:"owner_user_id"`
		KeyID        string `json:"key_id"`
		ModelID      string `json:"model_id"`
		ConnectionID string `json:"connection_id"`
		StartedAt    int64  `json:"started_at"`
		DeltaNanos   int64  `json:"delta_nanos"`
		UnknownDelta int64  `json:"unknown_delta"`
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return err
	}
	if item.OwnerUserID == "" || item.KeyID == "" || item.ModelID == "" || item.ConnectionID == "" || item.StartedAt == 0 {
		return errors.New("invalid cost adjustment projection")
	}
	date := time.UnixMilli(item.StartedAt).UTC().Format("2006-01-02")
	var cost, unknown int64
	if err := tx.QueryRowContext(ctx, `SELECT known_cost_nanos, unknown_attempts FROM usage_daily
		WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID).Scan(&cost, &unknown); err != nil {
		return errors.New("cost adjustment has no projected settlement")
	}
	nextCost, ok := checkedAdd(cost, item.DeltaNanos)
	if !ok || nextCost < 0 {
		return errors.New("projected cost overflow")
	}
	nextUnknown, ok := checkedAdd(unknown, item.UnknownDelta)
	if !ok || nextUnknown < 0 {
		return errors.New("projected unknown count overflow")
	}
	_, err := tx.ExecContext(ctx, `UPDATE usage_daily SET known_cost_nanos = ?, unknown_attempts = ?
		WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`, nextCost, nextUnknown, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID)
	return err
}

func projectSettlement(ctx context.Context, tx *sql.Tx, payload string, requestIncrement int64) error {
	var item struct {
		OwnerUserID              string `json:"owner_user_id"`
		KeyID                    string `json:"key_id"`
		ModelID                  string `json:"model_id"`
		ConnectionID             string `json:"connection_id"`
		StartedAt                int64  `json:"started_at"`
		UsageStatus              string `json:"usage_status"`
		InputTokens              *int64 `json:"input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		CacheCreation5mTokens    *int64 `json:"cache_creation_5m_input_tokens"`
		CacheCreation1hTokens    *int64 `json:"cache_creation_1h_input_tokens"`
		WebSearchCallCount       *int64 `json:"web_search_call_count"`
		CostNanos                *int64 `json:"cost_nanos"`
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return err
	}
	if item.OwnerUserID == "" || item.KeyID == "" || item.ModelID == "" || item.ConnectionID == "" || item.StartedAt == 0 {
		return errors.New("invalid settlement projection")
	}
	input, output, cacheCreation, cacheRead, cache5m, cache1h, webSearchCalls, cost, unknown := int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)
	if item.InputTokens != nil {
		input = *item.InputTokens
	}
	if item.OutputTokens != nil {
		output = *item.OutputTokens
	}
	if item.CacheCreationInputTokens != nil {
		cacheCreation = *item.CacheCreationInputTokens
	}
	if item.CacheReadInputTokens != nil {
		cacheRead = *item.CacheReadInputTokens
	}
	if item.CacheCreation5mTokens != nil {
		cache5m = *item.CacheCreation5mTokens
	}
	if item.CacheCreation1hTokens != nil {
		cache1h = *item.CacheCreation1hTokens
	}
	if item.WebSearchCallCount != nil {
		webSearchCalls = *item.WebSearchCallCount
	}
	if item.CostNanos != nil {
		cost = *item.CostNanos
	}
	if item.UsageStatus == "unknown" || item.CostNanos == nil {
		unknown = 1
	}
	date := time.UnixMilli(item.StartedAt).UTC().Format("2006-01-02")
	var currentRequests, currentInput, currentOutput, currentCacheCreation, currentCacheRead, currentCache5m, currentCache1h, currentWebSearchCalls, currentCost, currentUnknown int64
	err := tx.QueryRowContext(ctx, `SELECT requests, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, cache_creation_5m_input_tokens, cache_creation_1h_input_tokens, web_search_calls, known_cost_nanos, unknown_attempts FROM usage_daily
		WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID).
		Scan(&currentRequests, &currentInput, &currentOutput, &currentCacheCreation, &currentCacheRead, &currentCache5m, &currentCache1h, &currentWebSearchCalls, &currentCost, &currentUnknown)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_daily (date, owner_user_id, key_id, model_id, connection_id, requests, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, cache_creation_5m_input_tokens, cache_creation_1h_input_tokens, web_search_calls, known_cost_nanos, unknown_attempts)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID, requestIncrement, input, output, cacheCreation, cacheRead, cache5m, cache1h, webSearchCalls, cost, unknown)
		return err
	}
	if err != nil {
		return err
	}
	nextRequests, okRequests := checkedAdd(currentRequests, requestIncrement)
	nextInput, okInput := checkedAdd(currentInput, input)
	nextOutput, okOutput := checkedAdd(currentOutput, output)
	nextCacheCreation, okCacheCreation := checkedAdd(currentCacheCreation, cacheCreation)
	nextCacheRead, okCacheRead := checkedAdd(currentCacheRead, cacheRead)
	nextCache5m, okCache5m := checkedAdd(currentCache5m, cache5m)
	nextCache1h, okCache1h := checkedAdd(currentCache1h, cache1h)
	nextWebSearchCalls, okWebSearchCalls := checkedAdd(currentWebSearchCalls, webSearchCalls)
	nextCost, okCost := checkedAdd(currentCost, cost)
	nextUnknown, okUnknown := checkedAdd(currentUnknown, unknown)
	if !okRequests || !okInput || !okOutput || !okCacheCreation || !okCacheRead || !okCache5m || !okCache1h || !okWebSearchCalls || !okCost || !okUnknown {
		return errors.New("usage projection overflow")
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_daily SET requests = ?, input_tokens = ?, output_tokens = ?, cache_creation_input_tokens = ?, cache_read_input_tokens = ?, cache_creation_5m_input_tokens = ?, cache_creation_1h_input_tokens = ?, web_search_calls = ?, known_cost_nanos = ?, unknown_attempts = ?
		WHERE date = ? AND owner_user_id = ? AND key_id = ? AND model_id = ? AND connection_id = ?`,
		nextRequests, nextInput, nextOutput, nextCacheCreation, nextCacheRead, nextCache5m, nextCache1h, nextWebSearchCalls, nextCost, nextUnknown, date, item.OwnerUserID, item.KeyID, item.ModelID, item.ConnectionID)
	return err
}
