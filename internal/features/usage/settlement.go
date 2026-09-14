package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

type SettlementInput struct {
	IdempotencyKey        string
	State                 string
	UsageStatus           string
	InputTokens           *int64
	OutputTokens          *int64
	CostNanos             *int64
	ResponseToolCallCount int64
	ToolCallStatus        string
	FinalRequest          bool
}

type ReconciliationInput struct {
	InputTokens    *int64 `json:"input_tokens"`
	OutputTokens   *int64 `json:"output_tokens"`
	CostNanos      *int64 `json:"-"`
	UsageStatus    string `json:"usage_status"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (service *Service) CloseFailedRequest(ctx context.Context, requestID string) error {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := service.now().UnixMilli()
	if _, err = tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind='request' AND lease_id=?", requestID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE requests SET state='failed',finished_at=? WHERE id=? AND state='in_progress'", now, requestID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (service *Service) CancelBeforeDispatch(ctx context.Context, attemptID string) error {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requestID, ownerID, keyID, modelID, connectionID string
	var startedAt int64
	if err := tx.QueryRowContext(ctx, `SELECT attempts.request_id,requests.owner_user_id,requests.key_id,attempts.model_id,attempts.connection_id,attempts.started_at FROM attempts JOIN requests ON requests.id=attempts.request_id WHERE attempts.id=? AND attempts.state='reserved'`, attemptID).Scan(&requestID, &ownerID, &keyID, &modelID, &connectionID, &startedAt); err != nil {
		return err
	}
	now := service.now().UnixMilli()
	if err := service.releaseReservations(ctx, tx, attemptID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind='attempt' AND lease_id=?", attemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind='request' AND lease_id=?", requestID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE attempts SET state='cancelled_before_dispatch',usage_status='estimated',finished_at=? WHERE id=?", now, attemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE requests SET state='cancelled',finished_at=? WHERE id=?", now, requestID); err != nil {
		return err
	}
	if err := appendOutbox(ctx, tx, "request.cancelled_before_dispatch", requestID, attemptID, map[string]any{"owner_user_id": ownerID, "key_id": keyID, "model_id": modelID, "connection_id": connectionID, "started_at": startedAt}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) Settle(ctx context.Context, attemptID string, input SettlementInput) error {
	if input.ToolCallStatus == "" {
		input.ToolCallStatus = "none"
	}
	if input.IdempotencyKey == "" || !contains([]string{"succeeded", "failed", "cancelled", "interrupted_unknown"}, input.State) || !contains([]string{"provider_reported", "estimated", "unknown"}, input.UsageStatus) {
		return errors.New("invalid settlement")
	}
	if input.ResponseToolCallCount < 0 || !contains([]string{"none", "completed", "incomplete"}, input.ToolCallStatus) || input.ResponseToolCallCount == 0 && input.ToolCallStatus != "none" || input.ResponseToolCallCount > 0 && input.ToolCallStatus == "none" {
		return errors.New("invalid tool call settlement")
	}
	if input.InputTokens != nil && (*input.InputTokens < 0 || *input.InputTokens > maxSafeInteger) || input.OutputTokens != nil && (*input.OutputTokens < 0 || *input.OutputTokens > maxSafeInteger) || input.CostNanos != nil && *input.CostNanos < 0 {
		return errors.New("settlement values cannot be negative")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var settledAttemptID, entryType, signature string
	var settledTokens, settledCost sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT attempt_id, entry_type, token_units, cost_nanos, reason FROM usage_ledger WHERE idempotency_key = ?", input.IdempotencyKey).Scan(&settledAttemptID, &entryType, &settledTokens, &settledCost, &signature)
	if err == nil {
		if input.CostNanos == nil {
			input.CostNanos, _ = calculatedAttemptCost(ctx, tx, attemptID, input.InputTokens, input.OutputTokens)
		}
		tokens, sumErr := nullableInt64Sum(input.InputTokens, input.OutputTokens)
		if sumErr != nil {
			return sumErr
		}
		if settledAttemptID == attemptID && entryType == "settlement" && nullableEqual(settledTokens, tokens) && nullableEqual(settledCost, input.CostNanos) && signature == settlementSignature(input) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var requestID, state, ownerID, keyID, modelID, connectionID string
	var priceVersionID sql.NullString
	var startedAt int64
	if err := tx.QueryRowContext(ctx, `SELECT attempts.request_id, attempts.state, requests.owner_user_id, requests.key_id, attempts.model_id, attempts.connection_id, attempts.price_version_id, attempts.started_at
		FROM attempts JOIN requests ON requests.id = attempts.request_id WHERE attempts.id = ?`, attemptID).Scan(&requestID, &state, &ownerID, &keyID, &modelID, &connectionID, &priceVersionID, &startedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !contains([]string{"reserved", "dispatching", "streaming"}, state) {
		return ErrConflict
	}
	if input.CostNanos == nil && priceVersionID.Valid {
		input.CostNanos, err = calculatedPriceCost(ctx, tx, priceVersionID.String, input.InputTokens, input.OutputTokens)
		if err != nil {
			return err
		}
	}
	ledgerID, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	tokenUnits, err := nullableInt64Sum(input.InputTokens, input.OutputTokens)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO usage_ledger (id, attempt_id, entry_type, token_units, cost_nanos, idempotency_key, reason, created_at) VALUES (?, ?, 'settlement', ?, ?, ?, ?, ?)", "led_"+ledgerID, attemptID, tokenUnits, input.CostNanos, input.IdempotencyKey, settlementSignature(input), service.now().UnixMilli())
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT reservations.id, reservations.policy_id, reservations.period_start, reservations.reserved_units, limit_policies.metric, limit_policies.algorithm, limit_policies.limit_units
		FROM reservations JOIN limit_policies ON limit_policies.id = reservations.policy_id WHERE reservations.attempt_id = ? AND reservations.state = 'active'`, attemptID)
	if err != nil {
		return err
	}
	type reservation struct {
		id, policyID, metric, algorithm string
		period                          sql.NullInt64
		reserved, limit                 int64
	}
	var reservations []reservation
	for rows.Next() {
		var item reservation
		if err := rows.Scan(&item.id, &item.policyID, &item.period, &item.reserved, &item.metric, &item.algorithm, &item.limit); err != nil {
			rows.Close()
			return err
		}
		reservations = append(reservations, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := service.now().UnixMilli()
	for _, item := range reservations {
		actual, known := settlementUnits(item.metric, tokenUnits, input.CostNanos)
		if input.UsageStatus == "unknown" || !known {
			if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'uncertain', settled_at = ? WHERE id = ?", now, item.id); err != nil {
				return err
			}
			continue
		}
		if item.algorithm == "token_bucket" {
			var remaining int64
			if err := tx.QueryRowContext(ctx, "SELECT remaining_units FROM bucket_state WHERE policy_id = ?", item.policyID).Scan(&remaining); err != nil {
				return err
			}
			adjusted, ok := checkedAdd(remaining, item.reserved, -actual)
			if !ok {
				return errors.New("bucket settlement overflow")
			}
			if _, err := tx.ExecContext(ctx, "UPDATE bucket_state SET remaining_units = ? WHERE policy_id = ?", min(item.limit, adjusted), item.policyID); err != nil {
				return err
			}
		} else {
			if !item.period.Valid {
				return errors.New("quota reservation has no period")
			}
			if err := updateQuotaPeriod(ctx, tx, item.policyID, item.period.Int64, actual, -item.reserved); err != nil {
				return fmt.Errorf("quota reservation state is inconsistent: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'settled', settled_at = ? WHERE id = ?", now, item.id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET state = ?, usage_status = ?, input_tokens = ?, output_tokens = ?, as_recorded_cost_nanos = ?, restated_cost_nanos = ?, response_tool_call_count = ?, tool_call_status = ?, finished_at = ? WHERE id = ?`,
		input.State, input.UsageStatus, input.InputTokens, input.OutputTokens, input.CostNanos, input.CostNanos, input.ResponseToolCallCount, input.ToolCallStatus, now, attemptID)
	if err != nil {
		return err
	}
	if priceVersionID.Valid && input.CostNanos != nil {
		assessmentID, err := credentials.RandomToken(16)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cost_assessments (id, attempt_id, price_version_id, calculation_version, kind, amount_nanos, delta_nanos, created_at)
			VALUES (?, ?, ?, 1, 'recorded', ?, 0, ?)`, "ass_"+assessmentID, attemptID, priceVersionID.String, *input.CostNanos, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind = 'attempt' AND lease_id = ?", attemptID); err != nil {
		return err
	}
	if input.FinalRequest {
		if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind = 'request' AND lease_id = ?", requestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE requests SET state = ?, finished_at = ? WHERE id = ?", requestState(input.State), now, requestID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, "UPDATE requests SET state = 'in_progress' WHERE id = ?", requestID); err != nil {
		return err
	}
	if err := appendOutbox(ctx, tx, "attempt.settled", requestID, attemptID, map[string]any{"owner_user_id": ownerID, "key_id": keyID, "model_id": modelID, "connection_id": connectionID, "started_at": startedAt, "state": input.State, "usage_status": input.UsageStatus, "input_tokens": input.InputTokens, "output_tokens": input.OutputTokens, "cost_nanos": input.CostNanos}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func calculatedAttemptCost(ctx context.Context, tx *sql.Tx, attemptID string, inputTokens, outputTokens *int64) (*int64, error) {
	var priceID sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT price_version_id FROM attempts WHERE id = ?", attemptID).Scan(&priceID); err != nil || !priceID.Valid {
		return nil, err
	}
	return calculatedPriceCost(ctx, tx, priceID.String, inputTokens, outputTokens)
}

func calculatedPriceCost(ctx context.Context, tx *sql.Tx, priceID string, inputTokens, outputTokens *int64) (*int64, error) {
	if inputTokens == nil || outputTokens == nil {
		return nil, nil
	}
	var inputRate, outputRate int64
	if err := tx.QueryRowContext(ctx, "SELECT input_nanos_per_million, output_nanos_per_million FROM price_versions WHERE id = ?", priceID).Scan(&inputRate, &outputRate); err != nil {
		return nil, err
	}
	cost, err := CalculateCost(*inputTokens, *outputTokens, inputRate, outputRate)
	return &cost, err
}

func settlementSignature(input SettlementInput) string {
	return fmt.Sprintf("state=%s;usage=%s;final=%t;input=%s;output=%s;cost=%s;tools=%d;tool_status=%s", input.State, input.UsageStatus, input.FinalRequest, nullableValue(input.InputTokens), nullableValue(input.OutputTokens), nullableValue(input.CostNanos), input.ResponseToolCallCount, input.ToolCallStatus)
}

func nullableValue(value *int64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(*value)
}

func nullableEqual(stored sql.NullInt64, value *int64) bool {
	return stored.Valid == (value != nil) && (!stored.Valid || stored.Int64 == *value)
}

func updateQuotaPeriod(ctx context.Context, tx *sql.Tx, policyID string, periodStart, consumedDelta, reservedDelta int64) error {
	var consumed, reserved int64
	if err := tx.QueryRowContext(ctx, "SELECT consumed_units, reserved_units FROM quota_periods WHERE policy_id = ? AND period_start = ?", policyID, periodStart).Scan(&consumed, &reserved); err != nil {
		return err
	}
	nextConsumed, ok := checkedAdd(consumed, consumedDelta)
	if !ok || nextConsumed < 0 || nextConsumed > maxSafeInteger {
		return errors.New("consumed units overflow")
	}
	nextReserved, ok := checkedAdd(reserved, reservedDelta)
	if !ok || nextReserved < 0 || nextReserved > maxSafeInteger {
		return errors.New("reserved units overflow")
	}
	result, err := tx.ExecContext(ctx, `UPDATE quota_periods SET consumed_units = ?, reserved_units = ?
		WHERE policy_id = ? AND period_start = ? AND consumed_units = ? AND reserved_units = ?`, nextConsumed, nextReserved, policyID, periodStart, consumed, reserved)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("quota period changed concurrently")
	}
	return nil
}

func settlementUnits(metric string, tokens, cost *int64) (int64, bool) {
	if metric == "tokens" && tokens != nil {
		return *tokens, true
	}
	if metric == "spend" && cost != nil {
		return *cost, true
	}
	return 0, false
}

func nullableInt64Sum(left, right *int64) (*int64, error) {
	if left == nil || right == nil {
		return nil, nil
	}
	value, ok := checkedAdd(*left, *right)
	if !ok || value > maxSafeInteger {
		return nil, errors.New("settlement token count is too large")
	}
	return &value, nil
}

func requestState(attemptState string) string {
	if attemptState == "succeeded" {
		return "succeeded"
	}
	if attemptState == "cancelled" {
		return "cancelled"
	}
	if attemptState == "interrupted_unknown" {
		return "interrupted_unknown"
	}
	return "failed"
}

func (service *Service) FinalizeRequest(ctx context.Context, requestID, state string) error {
	if !contains([]string{"failed", "cancelled", "interrupted_unknown"}, state) {
		return errors.New("invalid final request state")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE request_id = ? AND state IN ('reserved', 'dispatching', 'streaming')`, requestID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE requests SET state = ?, finished_at = ? WHERE id = ? AND state = 'in_progress'", state, service.now().UnixMilli(), requestID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases WHERE lease_kind = 'request' AND request_id = ?", requestID); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) ReconcileUnknown(ctx context.Context, actor auth.User, attemptID string, input ReconciliationInput) error {
	if input.InputTokens == nil || input.OutputTokens == nil || *input.InputTokens < 0 || *input.InputTokens > maxSafeInteger || *input.OutputTokens < 0 || *input.OutputTokens > maxSafeInteger || input.CostNanos != nil && *input.CostNanos < 0 || !contains([]string{"provider_reported", "estimated"}, input.UsageStatus) || len(input.Reason) < 3 || len(input.Reason) > 500 || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 {
		return errors.New("known tokens, usage_status, reason, and idempotency_key are required")
	}
	tokens, err := nullableInt64Sum(input.InputTokens, input.OutputTokens)
	if err != nil {
		return err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return ErrDenied
	}
	var existingAttempt, entryType, existingReason string
	var existingTokens, existingCost sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT attempt_id, entry_type, token_units, cost_nanos, reason FROM usage_ledger WHERE idempotency_key = ?", input.IdempotencyKey).Scan(&existingAttempt, &entryType, &existingTokens, &existingCost, &existingReason)
	signature := fmt.Sprintf("reconcile:%s:%s:%s:%s:%s", input.UsageStatus, input.Reason, nullableValue(input.InputTokens), nullableValue(input.OutputTokens), nullableValue(input.CostNanos))
	if err == nil {
		if existingAttempt == attemptID && entryType == "reconciliation" && existingReason == signature {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var requestID, ownerID, keyID, modelID, connectionID, state, usageStatus string
	var priceID sql.NullString
	var previousInput, previousOutput, previousCost sql.NullInt64
	var startedAt int64
	if err := tx.QueryRowContext(ctx, `SELECT attempts.request_id, requests.owner_user_id, requests.key_id, attempts.model_id, attempts.connection_id, attempts.state, attempts.usage_status, attempts.price_version_id, attempts.started_at, attempts.input_tokens, attempts.output_tokens, COALESCE(attempts.restated_cost_nanos, attempts.as_recorded_cost_nanos)
		FROM attempts JOIN requests ON requests.id = attempts.request_id WHERE attempts.id = ?`, attemptID).Scan(&requestID, &ownerID, &keyID, &modelID, &connectionID, &state, &usageStatus, &priceID, &startedAt, &previousInput, &previousOutput, &previousCost); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != "interrupted_unknown" || usageStatus != "unknown" {
		var uncertain int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM reservations WHERE attempt_id = ? AND state = 'uncertain'", attemptID).Scan(&uncertain); err != nil {
			return err
		}
		if !contains([]string{"succeeded", "failed", "cancelled", "interrupted_unknown"}, state) || uncertain == 0 && previousInput.Valid && previousOutput.Valid && previousCost.Valid {
			return ErrConflict
		}
	}
	var priorReconciliations int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_ledger WHERE attempt_id = ? AND entry_type = 'reconciliation'", attemptID).Scan(&priorReconciliations); err != nil {
		return err
	}
	if priorReconciliations > 0 && (previousInput.Valid && previousInput.Int64 != *input.InputTokens || previousOutput.Valid && previousOutput.Int64 != *input.OutputTokens) {
		return ErrConflict
	}
	if input.CostNanos == nil && !previousCost.Valid && priceID.Valid {
		input.CostNanos, err = calculatedPriceCost(ctx, tx, priceID.String, input.InputTokens, input.OutputTokens)
		if err != nil {
			return err
		}
	}
	effectiveCost := input.CostNanos
	if effectiveCost == nil && previousCost.Valid {
		value := previousCost.Int64
		effectiveCost = &value
	}
	if actor.Role == "admin" && ownerID != actor.ID {
		var ownerRole string
		if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", ownerID).Scan(&ownerRole); err != nil {
			return err
		}
		if ownerRole != "member" {
			return ErrDenied
		}
	}
	if err := requireOutboxCapacity(ctx, tx, 1); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT reservations.id, reservations.policy_id, reservations.period_start, reservations.reserved_units, limit_policies.metric, limit_policies.algorithm, limit_policies.limit_units
		FROM reservations JOIN limit_policies ON limit_policies.id = reservations.policy_id WHERE reservations.attempt_id = ? AND reservations.state = 'uncertain'`, attemptID)
	if err != nil {
		return err
	}
	type reservation struct {
		id, policyID, metric, algorithm string
		period                          sql.NullInt64
		reserved, limit                 int64
	}
	var reservations []reservation
	for rows.Next() {
		var item reservation
		if err := rows.Scan(&item.id, &item.policyID, &item.period, &item.reserved, &item.metric, &item.algorithm, &item.limit); err != nil {
			rows.Close()
			return err
		}
		reservations = append(reservations, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range reservations {
		actual, known := settlementUnits(item.metric, tokens, effectiveCost)
		if !known {
			continue
		}
		if item.algorithm == "token_bucket" {
			var remaining int64
			if err := tx.QueryRowContext(ctx, "SELECT remaining_units FROM bucket_state WHERE policy_id = ?", item.policyID).Scan(&remaining); err != nil {
				return err
			}
			next, ok := checkedAdd(remaining, item.reserved, -actual)
			if !ok {
				return errors.New("bucket reconciliation overflow")
			}
			if _, err := tx.ExecContext(ctx, "UPDATE bucket_state SET remaining_units = ? WHERE policy_id = ?", min(item.limit, next), item.policyID); err != nil {
				return err
			}
		} else if !item.period.Valid {
			return errors.New("quota reconciliation has no period")
		} else if err := updateQuotaPeriod(ctx, tx, item.policyID, item.period.Int64, actual, -item.reserved); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'settled', settled_at = ? WHERE id = ?", service.now().UnixMilli(), item.id); err != nil {
			return err
		}
	}
	hasCost := effectiveCost != nil
	nextUsageStatus := "unknown"
	if hasCost {
		nextUsageStatus = input.UsageStatus
	}
	if _, err := tx.ExecContext(ctx, "UPDATE attempts SET usage_status = ?, input_tokens = ?, output_tokens = ?, as_recorded_cost_nanos = COALESCE(?, as_recorded_cost_nanos), restated_cost_nanos = COALESCE(?, restated_cost_nanos) WHERE id = ?", nextUsageStatus, input.InputTokens, input.OutputTokens, input.CostNanos, input.CostNanos, attemptID); err != nil {
		return err
	}
	inputDelta, okInput := checkedAdd(*input.InputTokens, -nullInt64Value(previousInput))
	outputDelta, okOutput := checkedAdd(*input.OutputTokens, -nullInt64Value(previousOutput))
	costDelta, okCost := int64(0), true
	wasUnknown := usageStatus == "unknown" || !previousCost.Valid
	unknownDelta := int64(0)
	if input.CostNanos != nil {
		costDelta, okCost = checkedAdd(*input.CostNanos, -nullInt64Value(previousCost))
	}
	if wasUnknown && nextUsageStatus != "unknown" && hasCost {
		unknownDelta = -1
	}
	if !okInput || !okOutput || !okCost {
		return errors.New("reconciliation delta overflow")
	}
	tokenDelta, ok := checkedAdd(inputDelta, outputDelta)
	if !ok {
		return errors.New("reconciliation token delta overflow")
	}
	var ledgerCost *int64
	if input.CostNanos != nil {
		ledgerCost = &costDelta
	}
	ledgerID, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO usage_ledger (id, attempt_id, entry_type, token_units, cost_nanos, idempotency_key, actor_user_id, reason, created_at) VALUES (?, ?, 'reconciliation', ?, ?, ?, ?, ?, ?)", "led_"+ledgerID, attemptID, tokenDelta, ledgerCost, input.IdempotencyKey, actor.ID, signature, service.now().UnixMilli()); err != nil {
		return err
	}
	if err := appendOutbox(ctx, tx, "attempt.reconciled", requestID, attemptID, map[string]any{"owner_user_id": ownerID, "key_id": keyID, "model_id": modelID, "connection_id": connectionID, "started_at": startedAt, "input_tokens": inputDelta, "output_tokens": outputDelta, "cost_nanos": costDelta, "unknown_delta": unknownDelta}, service.now().UnixMilli()); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, actor.ID, "usage.reconcile", "attempt", attemptID, map[string]any{"reason": input.Reason}); err != nil {
		return err
	}
	return tx.Commit()
}

func nullInt64Value(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func (service *Service) Recover(ctx context.Context) error {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT attempts.id, attempts.request_id, attempts.state, requests.owner_user_id, requests.key_id,
		attempts.model_id, attempts.connection_id, attempts.started_at FROM attempts JOIN requests ON requests.id = attempts.request_id
		WHERE attempts.state IN ('reserved', 'dispatching', 'streaming')`)
	if err != nil {
		return err
	}
	type interrupted struct {
		attemptID, requestID, state, ownerID, keyID, modelID, connectionID string
		startedAt                                                          int64
	}
	var items []interrupted
	for rows.Next() {
		var item interrupted
		if err := rows.Scan(&item.attemptID, &item.requestID, &item.state, &item.ownerID, &item.keyID, &item.modelID, &item.connectionID, &item.startedAt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := service.now().UnixMilli()
	for _, item := range items {
		if item.state == "reserved" {
			if err := service.releaseReservations(ctx, tx, item.attemptID, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE attempts SET state = 'cancelled_before_dispatch', usage_status = 'estimated', finished_at = ? WHERE id = ?", now, item.attemptID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE requests SET state = 'cancelled', finished_at = ? WHERE id = ?", now, item.requestID); err != nil {
				return err
			}
			if err := appendOutbox(ctx, tx, "request.cancelled_before_dispatch", item.requestID, item.attemptID, map[string]any{"owner_user_id": item.ownerID, "key_id": item.keyID, "model_id": item.modelID, "connection_id": item.connectionID, "started_at": item.startedAt}, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'uncertain', settled_at = ? WHERE attempt_id = ? AND state = 'active'", now, item.attemptID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE attempts SET state = 'interrupted_unknown', usage_status = 'unknown', finished_at = ? WHERE id = ?", now, item.attemptID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE requests SET state = 'interrupted_unknown', finished_at = ? WHERE id = ?", now, item.requestID); err != nil {
				return err
			}
			if err := appendOutbox(ctx, tx, "attempt.settled", item.requestID, item.attemptID, map[string]any{"owner_user_id": item.ownerID, "key_id": item.keyID, "model_id": item.modelID, "connection_id": item.connectionID, "started_at": item.startedAt, "state": "interrupted_unknown", "usage_status": "unknown", "input_tokens": nil, "output_tokens": nil, "cost_nanos": nil}, now); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'failed', finished_at = ? WHERE state = 'in_progress'
		AND NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id = requests.id AND attempts.state IN ('reserved', 'dispatching', 'streaming'))`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM concurrency_leases"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO admission_clock (singleton, last_effective_at, process_epoch) VALUES (1, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET last_effective_at = MAX(admission_clock.last_effective_at, excluded.last_effective_at), process_epoch = excluded.process_epoch`, now, service.processEpoch)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) releaseReservations(ctx context.Context, tx *sql.Tx, attemptID string, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT reservations.id, reservations.policy_id, reservations.period_start, reservations.reserved_units, limit_policies.algorithm, limit_policies.limit_units
		FROM reservations JOIN limit_policies ON limit_policies.id = reservations.policy_id WHERE attempt_id = ? AND state = 'active'`, attemptID)
	if err != nil {
		return err
	}
	type item struct {
		id, policyID, algorithm string
		period                  sql.NullInt64
		reserved, limit         int64
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.policyID, &value.period, &value.reserved, &value.algorithm, &value.limit); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		if item.algorithm == "token_bucket" {
			if _, err := tx.ExecContext(ctx, "UPDATE bucket_state SET remaining_units = MIN(?, remaining_units + ?) WHERE policy_id = ?", item.limit, item.reserved, item.policyID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE quota_periods SET reserved_units = reserved_units - ? WHERE policy_id = ? AND period_start = ? AND reserved_units >= ?", item.reserved, item.policyID, item.period.Int64, item.reserved); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE reservations SET state = 'released', settled_at = ? WHERE id = ?", now, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) MarkStreaming(ctx context.Context, attemptID string) error {
	result, err := service.database.ExecContext(ctx, "UPDATE attempts SET state = 'streaming' WHERE id = ? AND state = 'dispatching'", attemptID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("mark streaming: %w", ErrConflict)
	}
	return nil
}

func timeString(millis int64) string { return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano) }
