package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

type PriceVersion struct {
	ID                    string  `json:"id"`
	ConnectionID          string  `json:"connection_id"`
	ModelID               string  `json:"model_id"`
	InputUSDPerMillion    string  `json:"input_usd_per_million"`
	OutputUSDPerMillion   string  `json:"output_usd_per_million"`
	Source                string  `json:"source"`
	EffectiveFrom         string  `json:"effective_from"`
	EffectiveTo           *string `json:"effective_to"`
	CreatedAt             string  `json:"created_at"`
	inputNanosPerMillion  int64
	outputNanosPerMillion int64
	createdAt             int64
}

type PriceInput struct {
	ConnectionID        string `json:"connection_id"`
	ModelID             string `json:"model_id"`
	InputUSDPerMillion  string `json:"input_usd_per_million"`
	OutputUSDPerMillion string `json:"output_usd_per_million"`
	Source              string `json:"source"`
	EffectiveFrom       string `json:"effective_from"`
	EffectiveTo         string `json:"effective_to"`
}

func (service *Service) CreatePrice(ctx context.Context, actor auth.User, input PriceInput) (PriceVersion, error) {
	inputRate, err := ParseUSD(input.InputUSDPerMillion)
	if err != nil {
		return PriceVersion{}, fmt.Errorf("input price: %w", err)
	}
	outputRate, err := ParseUSD(input.OutputUSDPerMillion)
	if err != nil {
		return PriceVersion{}, fmt.Errorf("output price: %w", err)
	}
	from, err := time.Parse(time.RFC3339, input.EffectiveFrom)
	if err != nil {
		return PriceVersion{}, errors.New("effective_from must be RFC3339")
	}
	var to any
	toMillis := int64(mathMaxInt64)
	if input.EffectiveTo != "" {
		parsed, err := time.Parse(time.RFC3339, input.EffectiveTo)
		if err != nil || !parsed.After(from) {
			return PriceVersion{}, errors.New("effective_to must be later than effective_from")
		}
		to, toMillis = parsed.UnixMilli(), parsed.UnixMilli()
	}
	if input.ConnectionID == "" || input.ModelID == "" || input.Source == "" || len(input.ConnectionID) > 200 || len(input.ModelID) > 200 || len(input.Source) > 500 {
		return PriceVersion{}, errors.New("connection, model, and source are required")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return PriceVersion{}, err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return PriceVersion{}, err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return PriceVersion{}, ErrDenied
	}
	if input.EffectiveTo == "" {
		var previousID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM price_versions WHERE connection_id = ? AND model_id = ? AND effective_to IS NULL AND effective_from < ? ORDER BY effective_from DESC LIMIT 1`, input.ConnectionID, input.ModelID, from.UnixMilli()).Scan(&previousID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PriceVersion{}, err
		}
		if err == nil {
			var conflictingAttempts int64
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE price_version_id = ? AND started_at >= ?", previousID, from.UnixMilli()).Scan(&conflictingAttempts); err != nil {
				return PriceVersion{}, err
			}
			if conflictingAttempts != 0 {
				return PriceVersion{}, errors.New("new effective date would invalidate snapshotted attempts; use historical repricing")
			}
			if _, err := tx.ExecContext(ctx, "UPDATE price_versions SET effective_to = ? WHERE id = ? AND effective_to IS NULL", from.UnixMilli(), previousID); err != nil {
				return PriceVersion{}, err
			}
			if err := insertAudit(ctx, tx, actor.ID, "price.close", "price", previousID, map[string]any{"effective_to": from.UnixMilli()}); err != nil {
				return PriceVersion{}, err
			}
		}
	}
	var overlaps bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM price_versions WHERE connection_id = ? AND model_id = ? AND effective_from < ? AND (effective_to IS NULL OR effective_to > ?))`, input.ConnectionID, input.ModelID, toMillis, from.UnixMilli()).Scan(&overlaps); err != nil {
		return PriceVersion{}, err
	}
	if overlaps {
		return PriceVersion{}, errors.New("price interval overlaps an existing version")
	}
	id, err := credentials.RandomToken(16)
	if err != nil {
		return PriceVersion{}, err
	}
	id = "prc_" + id
	now := service.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO price_versions (id, connection_id, model_id, input_nanos_per_million, output_nanos_per_million, source, effective_from, effective_to, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, input.ConnectionID, input.ModelID, inputRate, outputRate, input.Source, from.UnixMilli(), to, actor.ID, now)
	if err != nil {
		return PriceVersion{}, err
	}
	if err := insertAudit(ctx, tx, actor.ID, "price.create", "price", id, map[string]any{"connection_id": input.ConnectionID, "model_id": input.ModelID, "source": input.Source}); err != nil {
		return PriceVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return PriceVersion{}, err
	}
	return service.GetPrice(ctx, actor, id)
}

const mathMaxInt64 = int64(^uint64(0) >> 1)

func (service *Service) GetPrice(ctx context.Context, actor auth.User, id string) (PriceVersion, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return PriceVersion{}, err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return PriceVersion{}, ErrDenied
	}
	price, err := scanPrice(service.database.QueryRowContext(ctx, `SELECT id, connection_id, model_id, input_nanos_per_million, output_nanos_per_million, source, effective_from, effective_to, created_at FROM price_versions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PriceVersion{}, ErrNotFound
	}
	return price, err
}

func (service *Service) ListPrices(ctx context.Context, actor auth.User, cursor string) ([]PriceVersion, string, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return nil, "", err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return nil, "", ErrDenied
	}
	before, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT id, connection_id, model_id, input_nanos_per_million, output_nanos_per_million, source, effective_from, effective_to, created_at FROM price_versions`
	args := []any{}
	if cursor != "" {
		query += " WHERE effective_from < ? OR (effective_from = ? AND id < ?)"
		args = append(args, before, before, beforeID)
	}
	query += " ORDER BY effective_from DESC, id DESC LIMIT 51"
	rows, err := service.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]PriceVersion, 0)
	for rows.Next() {
		item, err := scanPrice(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) <= 50 {
		return items, "", nil
	}
	items = items[:50]
	from, _ := time.Parse(time.RFC3339Nano, items[len(items)-1].EffectiveFrom)
	return items, encodeCursor(from.UnixMilli(), items[len(items)-1].ID), nil
}

func scanPrice(row scanner) (PriceVersion, error) {
	var item PriceVersion
	var from, created int64
	var to sql.NullInt64
	err := row.Scan(&item.ID, &item.ConnectionID, &item.ModelID, &item.inputNanosPerMillion, &item.outputNanosPerMillion, &item.Source, &from, &to, &created)
	item.createdAt = created
	item.InputUSDPerMillion, item.OutputUSDPerMillion = FormatUSD(item.inputNanosPerMillion), FormatUSD(item.outputNanosPerMillion)
	item.EffectiveFrom, item.CreatedAt = timeString(from), timeString(created)
	if to.Valid {
		value := timeString(to.Int64)
		item.EffectiveTo = &value
	}
	return item, err
}

func VerifiedFreePrice(inputNanos, outputNanos int64, source string, verifiedAt, now int64) bool {
	const freshness = 24 * time.Hour
	return inputNanos == 0 && outputNanos == 0 && strings.TrimSpace(source) != "" && verifiedAt <= now && verifiedAt >= now-freshness.Milliseconds()
}

func priceAt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, connectionID, modelID string, at int64) (PriceVersion, error) {
	return scanPrice(queryer.QueryRowContext(ctx, `SELECT id, connection_id, model_id, input_nanos_per_million, output_nanos_per_million, source, effective_from, effective_to, created_at
		FROM price_versions WHERE connection_id = ? AND model_id = ? AND effective_from <= ? AND (effective_to IS NULL OR effective_to > ?) ORDER BY effective_from DESC LIMIT 1`, connectionID, modelID, at, at))
}

type RepriceInput struct {
	ConnectionID   string `json:"connection_id"`
	ModelID        string `json:"model_id"`
	From           string `json:"from"`
	To             string `json:"to"`
	IdempotencyKey string `json:"idempotency_key"`
}

type RepricePreview struct {
	AffectedAttempts int64  `json:"affected_attempts"`
	MissingPrices    int64  `json:"missing_prices"`
	DeltaUSD         string `json:"delta_usd"`
}

type repriceItem struct {
	attemptID, priceID, ownerID, keyID, modelID, connectionID string
	startedAt, amount, delta                                  int64
	resolveUnknown                                            bool
}

func (service *Service) PreviewReprice(ctx context.Context, actor auth.User, input RepriceInput) (RepricePreview, error) {
	var err error
	actor, err = refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return RepricePreview{}, err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return RepricePreview{}, ErrDenied
	}
	from, to, err := repriceRange(input)
	if err != nil {
		return RepricePreview{}, err
	}
	items, missing, err := service.repriceItems(ctx, service.database, input.ConnectionID, input.ModelID, from, to)
	if err != nil {
		return RepricePreview{}, err
	}
	delta := int64(0)
	for _, item := range items {
		value, ok := checkedAdd(delta, item.delta)
		if !ok {
			return RepricePreview{}, errors.New("repricing delta is too large")
		}
		delta = value
	}
	return RepricePreview{AffectedAttempts: int64(len(items)), MissingPrices: missing, DeltaUSD: FormatUSD(delta)}, nil
}

func (service *Service) ApplyReprice(ctx context.Context, actor auth.User, input RepriceInput) (RepricePreview, error) {
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 {
		return RepricePreview{}, errors.New("idempotency_key is required")
	}
	from, to, err := repriceRange(input)
	if err != nil {
		return RepricePreview{}, err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return RepricePreview{}, err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return RepricePreview{}, err
	}
	if actor.Role != "owner" {
		return RepricePreview{}, ErrDenied
	}
	var existingActor, existingModel, existingConnection string
	var existingFrom, existingTo, existingAttempts, existingMissing, existingDelta int64
	err = tx.QueryRowContext(ctx, `SELECT actor_user_id, model_id, connection_id, from_time, to_time, affected_attempts, missing_prices, delta_nanos
		FROM pricing_jobs WHERE idempotency_key = ?`, input.IdempotencyKey).
		Scan(&existingActor, &existingModel, &existingConnection, &existingFrom, &existingTo, &existingAttempts, &existingMissing, &existingDelta)
	if err == nil {
		if existingActor != actor.ID || existingModel != input.ModelID || existingConnection != input.ConnectionID || existingFrom != from || existingTo != to {
			return RepricePreview{}, ErrConflict
		}
		return RepricePreview{AffectedAttempts: existingAttempts, MissingPrices: existingMissing, DeltaUSD: FormatUSD(existingDelta)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RepricePreview{}, err
	}
	items, missing, err := service.repriceItems(ctx, tx, input.ConnectionID, input.ModelID, from, to)
	if err != nil {
		return RepricePreview{}, err
	}
	if err := requireOutboxCapacity(ctx, tx, int64(len(items))); err != nil {
		return RepricePreview{}, err
	}
	delta := int64(0)
	applied := int64(0)
	for _, item := range items {
		assessmentID, err := credentials.RandomToken(16)
		if err != nil {
			return RepricePreview{}, err
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO cost_assessments (id, attempt_id, price_version_id, calculation_version, kind, amount_nanos, delta_nanos, created_at) VALUES (?, ?, ?, 1, 'restated', ?, ?, ?)`, "ass_"+assessmentID, item.attemptID, item.priceID, item.amount, item.delta, service.now().UnixMilli())
		if err != nil {
			return RepricePreview{}, err
		}
		inserted, _ := result.RowsAffected()
		if inserted == 0 {
			continue
		}
		ledgerID, err := credentials.RandomToken(16)
		if err != nil {
			return RepricePreview{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO usage_ledger (id, attempt_id, entry_type, cost_nanos, idempotency_key, actor_user_id, reason, created_at) VALUES (?, ?, 'reprice', ?, ?, ?, 'historical price version', ?)", "led_"+ledgerID, item.attemptID, item.delta, "reprice:"+item.attemptID+":"+item.priceID+":1", actor.ID, service.now().UnixMilli())
		if err != nil {
			return RepricePreview{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE attempts SET restated_cost_nanos = ? WHERE id = ?", item.amount, item.attemptID); err != nil {
			return RepricePreview{}, err
		}
		if err := reconcileSpendPeriods(ctx, tx, item.attemptID, item.delta, item.amount, service.now().UnixMilli()); err != nil {
			return RepricePreview{}, err
		}
		unknownDelta := int64(0)
		if item.resolveUnknown {
			unknownDelta = -1
		}
		if err := appendOutbox(ctx, tx, "attempt.cost_adjusted", "", item.attemptID, map[string]any{"owner_user_id": item.ownerID, "key_id": item.keyID, "model_id": item.modelID, "connection_id": item.connectionID, "started_at": item.startedAt, "delta_nanos": item.delta, "unknown_delta": unknownDelta}, service.now().UnixMilli()); err != nil {
			return RepricePreview{}, err
		}
		value, ok := checkedAdd(delta, item.delta)
		if !ok {
			return RepricePreview{}, errors.New("repricing delta is too large")
		}
		delta = value
		applied++
	}
	jobID, err := credentials.RandomToken(16)
	if err != nil {
		return RepricePreview{}, err
	}
	now := service.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO pricing_jobs (id, idempotency_key, actor_user_id, model_id, connection_id, from_time, to_time, state, affected_attempts, missing_prices, delta_nanos, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'completed', ?, ?, ?, ?, ?)`, "job_"+jobID, input.IdempotencyKey, actor.ID, input.ModelID, input.ConnectionID, from, to, applied, missing, delta, now, now)
	if err != nil {
		return RepricePreview{}, err
	}
	if err := insertAudit(ctx, tx, actor.ID, "usage.reprice", "pricing_job", "job_"+jobID, map[string]any{"affected_attempts": applied, "missing_prices": missing, "delta_nanos": delta}); err != nil {
		return RepricePreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return RepricePreview{}, err
	}
	return RepricePreview{AffectedAttempts: applied, MissingPrices: missing, DeltaUSD: FormatUSD(delta)}, nil
}

func repriceRange(input RepriceInput) (int64, int64, error) {
	from, err := time.Parse(time.RFC3339, input.From)
	if err != nil {
		return 0, 0, errors.New("from must be RFC3339")
	}
	to, err := time.Parse(time.RFC3339, input.To)
	if err != nil || !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return 0, 0, errors.New("to must be later than from and within 366 days")
	}
	if input.ConnectionID == "" || input.ModelID == "" || len(input.ConnectionID) > 200 || len(input.ModelID) > 200 {
		return 0, 0, errors.New("connection and model are required")
	}
	return from.UnixMilli(), to.UnixMilli(), nil
}

func (service *Service) repriceItems(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, connectionID, modelID string, from, to int64) ([]repriceItem, int64, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT attempts.id, requests.owner_user_id, requests.key_id, attempts.model_id, attempts.connection_id,
		attempts.input_tokens, attempts.output_tokens, COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos, 0), attempts.started_at,
		COALESCE((SELECT SUM(cost_nanos) FROM usage_ledger WHERE usage_ledger.attempt_id = attempts.id AND entry_type = 'adjustment'), 0),
		attempts.usage_status, attempts.restated_cost_nanos IS NULL AND attempts.as_recorded_cost_nanos IS NULL
		FROM attempts JOIN requests ON requests.id = attempts.request_id
		WHERE attempts.connection_id = ? AND attempts.model_id = ? AND attempts.started_at >= ? AND attempts.started_at < ? AND attempts.input_tokens IS NOT NULL AND attempts.output_tokens IS NOT NULL AND attempts.cache_creation_input_tokens IS NULL AND attempts.cache_read_input_tokens IS NULL AND attempts.cache_creation_5m_input_tokens IS NULL AND attempts.cache_creation_1h_input_tokens IS NULL AND attempts.web_search_max_calls IS NULL AND attempts.usage_status != 'unknown' ORDER BY attempts.started_at, attempts.id LIMIT 10001`, connectionID, modelID, from, to)
	if err != nil {
		return nil, 0, err
	}
	type rawItem struct {
		attemptID, ownerID, keyID, modelID, connectionID          string
		inputTokens, outputTokens, previous, started, adjustments int64
		usageStatus                                               string
		costUnknown                                               bool
	}
	var rawItems []rawItem
	for rows.Next() {
		if len(rawItems) >= 10_000 {
			rows.Close()
			return nil, 0, errors.New("repricing range exceeds 10000 attempts")
		}
		var item rawItem
		if err := rows.Scan(&item.attemptID, &item.ownerID, &item.keyID, &item.modelID, &item.connectionID, &item.inputTokens, &item.outputTokens, &item.previous, &item.started, &item.adjustments, &item.usageStatus, &item.costUnknown); err != nil {
			rows.Close()
			return nil, 0, err
		}
		rawItems = append(rawItems, item)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	var items []repriceItem
	missing := int64(0)
	for _, item := range rawItems {
		price, err := priceAt(ctx, queryer, connectionID, modelID, item.started)
		if errors.Is(err, sql.ErrNoRows) {
			missing++
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		amount, err := CalculateCost(item.inputTokens, item.outputTokens, price.inputNanosPerMillion, price.outputNanosPerMillion)
		if err != nil {
			return nil, 0, err
		}
		target, ok := checkedAdd(amount, item.adjustments)
		if !ok || target < 0 {
			return nil, 0, errors.New("repriced cost is too large")
		}
		items = append(items, repriceItem{attemptID: item.attemptID, priceID: price.ID, ownerID: item.ownerID, keyID: item.keyID, modelID: item.modelID, connectionID: item.connectionID, startedAt: item.started, amount: target, delta: target - item.previous, resolveUnknown: item.costUnknown && item.usageStatus != "unknown"})
	}
	return items, missing, nil
}

func reconcileSpendPeriods(ctx context.Context, tx *sql.Tx, attemptID string, delta, actual, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT reservations.id, reservations.policy_id, reservations.period_start, reservations.reserved_units, reservations.state
		FROM reservations JOIN limit_policies ON limit_policies.id = reservations.policy_id
		WHERE reservations.attempt_id = ? AND reservations.state IN ('settled', 'uncertain') AND limit_policies.metric = 'spend' AND reservations.period_start IS NOT NULL`, attemptID)
	if err != nil {
		return err
	}
	type period struct {
		id, policyID, state string
		start, reserved     int64
	}
	var periods []period
	for rows.Next() {
		var item period
		if err := rows.Scan(&item.id, &item.policyID, &item.start, &item.reserved, &item.state); err != nil {
			rows.Close()
			return err
		}
		periods = append(periods, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range periods {
		change, release := delta, int64(0)
		if item.state == "uncertain" {
			change, release = actual, item.reserved
		}
		if change == 0 && release == 0 {
			continue
		}
		if err := updateQuotaPeriod(ctx, tx, item.policyID, item.start, change, -release); err != nil {
			return errors.New("repricing would corrupt a spend period")
		}
		if item.state == "uncertain" {
			if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'settled', settled_at = ? WHERE id = ? AND state = 'uncertain'", now, item.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) AdjustCost(ctx context.Context, actor auth.User, attemptID, deltaUSD, reason, idempotencyKey string) (string, error) {
	if len(reason) < 3 || len(reason) > 500 || idempotencyKey == "" || len(idempotencyKey) > 200 {
		return "", errors.New("reason and idempotency_key are required")
	}
	delta, err := parseSignedUSD(deltaUSD)
	if err != nil || delta == 0 {
		return "", errors.New("delta_usd must be a non-zero USD decimal")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return "", err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return "", ErrDenied
	}
	var ownerID, keyID, modelID, connectionID, state, usageStatus string
	var startedAt int64
	var current int64
	var costUnknown bool
	err = tx.QueryRowContext(ctx, `SELECT requests.owner_user_id, requests.key_id, attempts.model_id, attempts.connection_id, attempts.started_at, attempts.state, attempts.usage_status,
		COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos, 0), attempts.restated_cost_nanos IS NULL AND attempts.as_recorded_cost_nanos IS NULL
		FROM attempts JOIN requests ON requests.id = attempts.request_id WHERE attempts.id = ?`, attemptID).
		Scan(&ownerID, &keyID, &modelID, &connectionID, &startedAt, &state, &usageStatus, &current, &costUnknown)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !contains([]string{"succeeded", "failed", "cancelled", "interrupted_unknown"}, state) {
		return "", ErrConflict
	}
	if usageStatus == "unknown" {
		return "", errors.New("reconcile unknown usage before adjusting its cost")
	}
	if actor.Role == "admin" && ownerID != actor.ID {
		var role string
		if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", ownerID).Scan(&role); err != nil {
			return "", err
		}
		if role != "member" {
			return "", ErrDenied
		}
	}
	var existingAttempt, existingType, existingActor, existingReason string
	var existingDelta sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT attempt_id, entry_type, actor_user_id, reason, cost_nanos FROM usage_ledger WHERE idempotency_key = ?", idempotencyKey).
		Scan(&existingAttempt, &existingType, &existingActor, &existingReason, &existingDelta)
	if err == nil {
		if existingAttempt == attemptID && existingType == "adjustment" && existingActor == actor.ID && existingReason == reason && existingDelta.Valid && existingDelta.Int64 == delta {
			return FormatUSD(current), nil
		}
		return "", ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	next, ok := checkedAdd(current, delta)
	if !ok || next < 0 {
		return "", errors.New("adjustment would make cost negative or overflow")
	}
	if err := requireOutboxCapacity(ctx, tx, 1); err != nil {
		return "", err
	}
	ledgerID, err := credentials.RandomToken(16)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO usage_ledger (id, attempt_id, entry_type, cost_nanos, idempotency_key, actor_user_id, reason, created_at) VALUES (?, ?, 'adjustment', ?, ?, ?, ?, ?)", "led_"+ledgerID, attemptID, delta, idempotencyKey, actor.ID, reason, service.now().UnixMilli())
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE attempts SET restated_cost_nanos = ? WHERE id = ?", next, attemptID); err != nil {
		return "", err
	}
	if err := reconcileSpendPeriods(ctx, tx, attemptID, delta, next, service.now().UnixMilli()); err != nil {
		return "", err
	}
	if err := insertAudit(ctx, tx, actor.ID, "usage.adjust", "attempt", attemptID, map[string]any{"delta_nanos": delta, "reason": reason}); err != nil {
		return "", err
	}
	unknownDelta := int64(0)
	if costUnknown && usageStatus != "unknown" {
		unknownDelta = -1
	}
	if err := appendOutbox(ctx, tx, "attempt.cost_adjusted", "", attemptID, map[string]any{"owner_user_id": ownerID, "key_id": keyID, "model_id": modelID, "connection_id": connectionID, "started_at": startedAt, "delta_nanos": delta, "unknown_delta": unknownDelta}, service.now().UnixMilli()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return FormatUSD(next), nil
}
