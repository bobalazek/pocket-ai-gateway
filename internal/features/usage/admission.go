package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"path"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	maxPendingOutboxEvents = 10_000
	maxPendingOutboxBytes  = 64 << 20
	outboxEventAllowance   = 1024
)

type AdmissionInput struct {
	RequestID              string
	KeyID                  string
	ConnectionID           string
	ModelID                string
	UpstreamModelRecordID  string
	UpstreamModelID        string
	ConnectionRevision     int64
	ModelRevision          int64
	Operation              string
	TargetOperation        string
	Scope                  string
	Dialect                string
	TargetDialect          string
	TranslationApplied     bool
	RequestToolCount       int64
	WebSearchMaxCalls      int64
	SelectionReason        string
	RejectedCandidatesJSON string
	RequiredPriceVersionID string
	RequireFreePrice       bool
	PriceUnavailable       bool
	BodyBytes              int64
	BatchItems             int64
	EstimatedInputTokens   int64
	EstimatedOutputTokens  int64
	EnforceOutputBound     bool
	OutputBounded          bool
}

type Admission struct {
	RequestID  string `json:"request_id"`
	AttemptID  string `json:"attempt_id"`
	Ordinal    int64  `json:"ordinal"`
	AdmittedAt string `json:"admitted_at"`
}

type Denial struct {
	PolicyID         string `json:"policy_id"`
	Metric           string `json:"metric"`
	Reason           string `json:"reason"`
	RetryAfterSecond *int64 `json:"retry_after_seconds,omitempty"`
}

func (denial *Denial) Error() string { return denial.Reason }

type runtimePolicy struct {
	ID, ScopeKind, Metric, Algorithm, Period     string
	WindowSeconds, Limit, Refill, RefillInterval int64
}

type counterPlan struct {
	policy                                           runtimePolicy
	periodStart                                      int64
	periodEnd                                        *int64
	consumed, reserved                               int64
	bucketRemaining, bucketRemainder, bucketRefillAt int64
	units                                            int64
	reserve                                          bool
	leaseKind, leaseID                               string
}

func (service *Service) Admit(ctx context.Context, input AdmissionInput) (Admission, error) {
	if input.TargetOperation == "" {
		input.TargetOperation = input.Operation
	}
	if input.TargetDialect == "" {
		input.TargetDialect = input.Dialect
	}
	if input.RejectedCandidatesJSON == "" {
		input.RejectedCandidatesJSON = "[]"
	}
	if input.KeyID == "" || input.ConnectionID == "" || input.ModelID == "" || input.Operation == "" || input.TargetOperation == "" || input.Scope == "" || input.Dialect == "" || input.TargetDialect == "" || len(input.KeyID) > 200 || len(input.ConnectionID) > 200 || len(input.ModelID) > 200 || len(input.Operation) > 100 || len(input.TargetOperation) > 300 || len(input.Scope) > 100 || len(input.Dialect) > 50 || len(input.TargetDialect) > 50 || len(input.SelectionReason) > 500 || len(input.RejectedCandidatesJSON) > 16_384 || len(input.RequiredPriceVersionID) > 200 || input.RequireFreePrice && input.RequiredPriceVersionID == "" || !json.Valid([]byte(input.RejectedCandidatesJSON)) || input.BodyBytes < 0 || input.BatchItems < 0 || input.RequestToolCount < 0 || input.WebSearchMaxCalls < 0 || input.WebSearchMaxCalls > 4 || input.EstimatedInputTokens < 0 || input.EstimatedOutputTokens < 0 {
		return Admission{}, errors.New("invalid admission input")
	}
	input.PriceUnavailable = input.PriceUnavailable || input.WebSearchMaxCalls > 0
	estimatedTokens, ok := checkedAdd(input.EstimatedInputTokens, input.EstimatedOutputTokens)
	if !ok {
		return Admission{}, errors.New("estimated token count is too large")
	}
	requestPart, err := credentials.RandomToken(16)
	if err != nil {
		return Admission{}, err
	}
	attemptPart, err := credentials.RandomToken(16)
	if err != nil {
		return Admission{}, err
	}
	requestID, firstAttempt := input.RequestID, input.RequestID == ""
	if firstAttempt {
		requestID = "req_" + requestPart
	}
	attemptID := "att_" + attemptPart
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return Admission{}, err
	}
	defer tx.Rollback()
	effective, err := service.effectiveTimeTx(ctx, tx)
	if err != nil {
		return Admission{}, err
	}

	var ownerID, keyScopesJSON, keyModelsJSON, keyConnectionsJSON, userScopesJSON, userModelsJSON, userConnectionsJSON string
	var unrestricted bool
	err = tx.QueryRowContext(ctx, `SELECT api_keys.owner_user_id, api_keys.scopes_json, api_keys.model_patterns_json, api_keys.connection_ids_json,
		users.inference_unrestricted, users.scopes_json, users.model_patterns_json, users.connection_ids_json
		FROM api_keys JOIN users ON users.id = api_keys.owner_user_id
		WHERE api_keys.id = ? AND api_keys.state = 'active' AND (api_keys.expires_at IS NULL OR api_keys.expires_at > ?) AND users.status = 'active'`, input.KeyID, effective).
		Scan(&ownerID, &keyScopesJSON, &keyModelsJSON, &keyConnectionsJSON, &unrestricted, &userScopesJSON, &userModelsJSON, &userConnectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Admission{}, ErrDenied
	}
	if err != nil {
		return Admission{}, err
	}
	if allowed, err := admissionGranted(input, unrestricted, keyScopesJSON, keyModelsJSON, keyConnectionsJSON, userScopesJSON, userModelsJSON, userConnectionsJSON); err != nil {
		return Admission{}, err
	} else if !allowed {
		return Admission{}, ErrDenied
	}
	if input.UpstreamModelRecordID != "" {
		var current bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public_models JOIN public_model_targets ON public_model_targets.public_model_id=public_models.id JOIN upstream_models ON upstream_models.id=public_model_targets.upstream_model_id JOIN provider_connections ON provider_connections.id=upstream_models.connection_id WHERE public_models.id=? AND upstream_models.id=? AND provider_connections.id=? AND public_models.revision=? AND upstream_models.upstream_id=? AND public_models.active=1 AND public_model_targets.enabled=1 AND upstream_models.active=1 AND provider_connections.enabled=1 AND provider_connections.revision=?)`, input.ModelID, input.UpstreamModelRecordID, input.ConnectionID, input.ModelRevision, input.UpstreamModelID, input.ConnectionRevision).Scan(&current)
		if err != nil || !current {
			return Admission{}, ErrDenied
		}
	}
	ordinal := int64(1)
	if !firstAttempt {
		var existingKey, existingOwner, model, operation, dialect, state string
		if err := tx.QueryRowContext(ctx, "SELECT key_id, owner_user_id, model_id, operation, dialect, state FROM requests WHERE id = ?", requestID).Scan(&existingKey, &existingOwner, &model, &operation, &dialect, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Admission{}, ErrNotFound
			}
			return Admission{}, err
		}
		if existingKey != input.KeyID || existingOwner != ownerID || model != input.ModelID || operation != input.Operation || dialect != input.Dialect || state != "in_progress" {
			return Admission{}, ErrDenied
		}
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(ordinal), 0) + 1 FROM attempts WHERE request_id = ?", requestID).Scan(&ordinal); err != nil {
			return Admission{}, err
		}
	}
	if err := requireOutboxCapacity(ctx, tx, 2); err != nil {
		return Admission{}, err
	}
	policies, err := matchingPolicies(ctx, tx, ownerID, input.KeyID, input.ConnectionID)
	if err != nil {
		return Admission{}, err
	}
	if input.EnforceOutputBound && !input.OutputBounded {
		for _, policy := range policies {
			if policy.Metric == "tokens" || policy.Metric == "output_tokens" || policy.Metric == "spend" {
				return Admission{}, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "an explicit output token limit is required by the active policy"}
			}
		}
	}
	var estimatedCost *int64
	priceVersionID := ""
	priceErr := sql.ErrNoRows
	if !input.PriceUnavailable {
		price, lookupErr := priceAt(ctx, tx, input.ConnectionID, input.ModelID, effective)
		priceErr = lookupErr
		if priceErr == nil {
			if input.RequireFreePrice && (price.ID != input.RequiredPriceVersionID || !VerifiedFreePrice(price.inputNanosPerMillion, price.outputNanosPerMillion, price.Source, price.createdAt, effective)) {
				return Admission{}, ErrDenied
			}
			cost, err := CalculateCost(input.EstimatedInputTokens, input.EstimatedOutputTokens, price.inputNanosPerMillion, price.outputNanosPerMillion)
			if err != nil {
				return Admission{}, err
			}
			estimatedCost, priceVersionID = &cost, price.ID
		} else if !errors.Is(priceErr, sql.ErrNoRows) {
			return Admission{}, priceErr
		}
	}
	if input.RequireFreePrice && priceErr != nil {
		return Admission{}, ErrDenied
	}
	for _, policy := range policies {
		if policy.Metric != "spend" {
			continue
		}
		if estimatedCost == nil {
			return Admission{}, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "a current price is required"}
		}
		break
	}
	plans := make([]counterPlan, 0, len(policies))
	for _, policy := range policies {
		plan, applies, err := evaluatePolicy(ctx, tx, policy, effective, firstAttempt, requestID, attemptID, input, estimatedTokens, estimatedCost)
		if err != nil {
			return Admission{}, err
		}
		if applies {
			plans = append(plans, plan)
		}
	}
	if firstAttempt {
		_, err = tx.ExecContext(ctx, "INSERT INTO requests (id, owner_user_id, key_id, operation, dialect, model_id, state, started_at) VALUES (?, ?, ?, ?, ?, ?, 'reserved', ?)", requestID, ownerID, input.KeyID, input.Operation, input.Dialect, input.ModelID, effective)
		if err != nil {
			return Admission{}, err
		}
	} else if _, err := tx.ExecContext(ctx, "UPDATE requests SET state = 'reserved', finished_at = NULL WHERE id = ?", requestID); err != nil {
		return Admission{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempts (id, request_id, ordinal, connection_id, model_id, upstream_model_record_id, upstream_model_id, connection_revision, target_dialect, target_operation, translation_applied, request_tool_count, web_search_max_calls, selection_reason, rejected_candidates_json, price_version_id, state, estimated_tokens, estimated_cost_nanos, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?, NULLIF(?, ''), 'reserved', ?, ?, ?)`, attemptID, requestID, ordinal, input.ConnectionID, input.ModelID, input.UpstreamModelRecordID, input.UpstreamModelID, input.ConnectionRevision, input.TargetDialect, input.TargetOperation, input.TranslationApplied, input.RequestToolCount, input.WebSearchMaxCalls, input.SelectionReason, input.RejectedCandidatesJSON, priceVersionID, estimatedTokens, estimatedCost, effective)
	if err != nil {
		return Admission{}, err
	}
	for _, plan := range plans {
		if err := applyPlan(ctx, tx, plan, requestID, attemptID, service.processEpoch, effective); err != nil {
			return Admission{}, err
		}
	}
	if err := appendOutbox(ctx, tx, "attempt.reserved", requestID, attemptID, map[string]any{"owner_user_id": ownerID, "key_id": input.KeyID, "connection_id": input.ConnectionID, "model_id": input.ModelID, "ordinal": ordinal}, effective); err != nil {
		return Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admission{}, err
	}
	return Admission{RequestID: requestID, AttemptID: attemptID, Ordinal: ordinal, AdmittedAt: time.UnixMilli(effective).UTC().Format(time.RFC3339Nano)}, nil
}

func matchingPolicies(ctx context.Context, tx *sql.Tx, userID, keyID, connectionID string) ([]runtimePolicy, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, scope_kind, metric, algorithm, period, window_seconds, limit_units, refill_units, refill_interval_ms
		FROM limit_policies WHERE enabled = 1 AND ((scope_kind = 'instance' AND scope_id = '') OR (scope_kind = 'user' AND scope_id = ?) OR (scope_kind = 'key' AND scope_id = ?) OR (scope_kind = 'connection' AND scope_id = ?))
		ORDER BY id`, userID, keyID, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []runtimePolicy
	for rows.Next() {
		var item runtimePolicy
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.Metric, &item.Algorithm, &item.Period, &item.WindowSeconds, &item.Limit, &item.Refill, &item.RefillInterval); err != nil {
			return nil, err
		}
		policies = append(policies, item)
	}
	return policies, rows.Err()
}

func admissionGranted(input AdmissionInput, unrestricted bool, keyScopesJSON, keyModelsJSON, keyConnectionsJSON, userScopesJSON, userModelsJSON, userConnectionsJSON string) (bool, error) {
	var keyScopes, keyModels, keyConnections, userScopes, userModels, userConnections []string
	encoded := []string{keyScopesJSON, keyModelsJSON, keyConnectionsJSON, userScopesJSON, userModelsJSON, userConnectionsJSON}
	targets := []*[]string{&keyScopes, &keyModels, &keyConnections, &userScopes, &userModels, &userConnections}
	for index := range encoded {
		if err := json.Unmarshal([]byte(encoded[index]), targets[index]); err != nil {
			return false, err
		}
	}
	if !stringContains(keyScopes, input.Scope) || !stringContains(keyConnections, input.ConnectionID) || !modelAllowed(keyModels, input.ModelID) {
		return false, nil
	}
	return unrestricted || stringContains(userScopes, input.Scope) && stringContains(userConnections, input.ConnectionID) && modelAllowed(userModels, input.ModelID), nil
}

func stringContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func modelAllowed(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, model); err == nil && matched {
			return true
		}
	}
	return false
}

func evaluatePolicy(ctx context.Context, tx *sql.Tx, policy runtimePolicy, now int64, first bool, requestID, attemptID string, input AdmissionInput, estimatedTokens int64, estimatedCost *int64) (counterPlan, bool, error) {
	plan := counterPlan{policy: policy}
	switch policy.Metric {
	case "requests":
		if !first && policy.ScopeKind != "connection" {
			return plan, false, nil
		}
		plan.units = 1
	case "tokens":
		plan.units, plan.reserve = estimatedTokens, true
	case "spend":
		if estimatedCost == nil {
			return plan, false, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "a bounded price estimate is required"}
		}
		plan.units, plan.reserve = *estimatedCost, true
	case "body_bytes":
		plan.units = input.BodyBytes
	case "output_tokens":
		plan.units = input.EstimatedOutputTokens
	case "batch_items":
		plan.units = input.BatchItems
	case "concurrency":
		plan.leaseKind, plan.leaseID = "request", requestID
		if policy.ScopeKind == "connection" {
			plan.leaseKind, plan.leaseID = "attempt", attemptID
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM concurrency_leases WHERE policy_id = ? AND lease_kind = ? AND lease_id = ?)", policy.ID, plan.leaseKind, plan.leaseID).Scan(&exists); err != nil {
			return plan, false, err
		}
		if exists {
			return plan, false, nil
		}
		var active int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM concurrency_leases WHERE policy_id = ?", policy.ID).Scan(&active); err != nil {
			return plan, false, err
		}
		if active >= policy.Limit {
			return plan, false, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "concurrency limit reached"}
		}
		return plan, true, nil
	}
	if policy.Algorithm == "ceiling" {
		if plan.units > policy.Limit {
			return plan, false, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "request ceiling exceeded"}
		}
		return plan, false, nil
	}
	if plan.units < 0 {
		return plan, false, errors.New("reservation units cannot be negative")
	}
	if plan.units == 0 && !plan.reserve {
		return plan, false, nil
	}
	if policy.Algorithm == "token_bucket" {
		var remaining, remainder, lastRefill, lastEffective int64
		err := tx.QueryRowContext(ctx, "SELECT remaining_units, refill_remainder, last_refill_at, last_effective_at FROM bucket_state WHERE policy_id = ?", policy.ID).Scan(&remaining, &remainder, &lastRefill, &lastEffective)
		if errors.Is(err, sql.ErrNoRows) {
			remaining, lastRefill, lastEffective = policy.Limit, now, now
		} else if err != nil {
			return plan, false, err
		}
		effective := max(now, lastEffective)
		elapsed := max(int64(0), effective-lastRefill)
		refill, nextRemainder, err := refillAmount(elapsed, policy.Refill, policy.RefillInterval, remainder)
		if err != nil {
			return plan, false, err
		}
		refilled := saturatingAdd(remaining, refill)
		if refilled >= policy.Limit {
			remaining, nextRemainder = policy.Limit, 0
		} else {
			remaining = refilled
		}
		if plan.units > remaining {
			retry := retryAfter(plan.units-remaining, policy.Refill, policy.RefillInterval)
			return plan, false, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "token bucket exhausted", RetryAfterSecond: retry}
		}
		plan.bucketRemaining, plan.bucketRemainder, plan.bucketRefillAt = remaining-plan.units, nextRemainder, effective
		return plan, true, nil
	}
	start, end := periodBounds(policy, now)
	plan.periodStart, plan.periodEnd = start, end
	var lastEffective int64
	err := tx.QueryRowContext(ctx, "SELECT consumed_units, reserved_units, last_effective_at FROM quota_periods WHERE policy_id = ? AND period_start = ?", policy.ID, start).Scan(&plan.consumed, &plan.reserved, &lastEffective)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return plan, false, err
	}
	used, ok := checkedAdd(plan.consumed, plan.reserved, plan.units)
	if !ok {
		return plan, false, errors.New("policy counter overflow")
	}
	if used > policy.Limit {
		var retry *int64
		if end != nil {
			seconds := max(int64(1), (*end-now+999)/1000)
			retry = &seconds
		}
		return plan, false, &Denial{PolicyID: policy.ID, Metric: policy.Metric, Reason: "policy allowance exhausted", RetryAfterSecond: retry}
	}
	if plan.reserve {
		plan.reserved += plan.units
	} else {
		plan.consumed += plan.units
	}
	return plan, true, nil
}

func applyPlan(ctx context.Context, tx *sql.Tx, plan counterPlan, requestID, attemptID, epoch string, now int64) error {
	if plan.policy.Metric == "concurrency" {
		var attempt any
		if plan.leaseKind == "attempt" {
			attempt = attemptID
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO concurrency_leases (policy_id, lease_kind, lease_id, request_id, attempt_id, process_epoch, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", plan.policy.ID, plan.leaseKind, plan.leaseID, requestID, attempt, epoch, now)
		return err
	}
	if plan.policy.Algorithm == "token_bucket" {
		_, err := tx.ExecContext(ctx, `INSERT INTO bucket_state (policy_id, remaining_units, refill_remainder, last_refill_at, last_effective_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(policy_id) DO UPDATE SET remaining_units = excluded.remaining_units, refill_remainder = excluded.refill_remainder, last_refill_at = excluded.last_refill_at, last_effective_at = excluded.last_effective_at`, plan.policy.ID, plan.bucketRemaining, plan.bucketRemainder, plan.bucketRefillAt, plan.bucketRefillAt)
		if err != nil {
			return err
		}
	} else {
		_, err := tx.ExecContext(ctx, `INSERT INTO quota_periods (policy_id, period_start, period_end, consumed_units, reserved_units, last_effective_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(policy_id, period_start) DO UPDATE SET consumed_units = excluded.consumed_units, reserved_units = excluded.reserved_units, last_effective_at = MAX(quota_periods.last_effective_at, excluded.last_effective_at)`, plan.policy.ID, plan.periodStart, plan.periodEnd, plan.consumed, plan.reserved, now)
		if err != nil {
			return err
		}
	}
	if plan.reserve {
		id, err := credentials.RandomToken(16)
		if err != nil {
			return err
		}
		var period any
		if plan.policy.Algorithm != "token_bucket" {
			period = plan.periodStart
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO reservations (id, attempt_id, policy_id, period_start, reserved_units, state, created_at) VALUES (?, ?, ?, ?, ?, 'active', ?)", "res_"+id, attemptID, plan.policy.ID, period, plan.units, now)
		return err
	}
	return nil
}

func (service *Service) effectiveTime(ctx context.Context) (int64, error) {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	effective, err := service.effectiveTimeTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	return effective, tx.Commit()
}

func (service *Service) effectiveTimeTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	wall := service.now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO admission_clock (singleton, last_effective_at, process_epoch) VALUES (1, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET last_effective_at = MAX(admission_clock.last_effective_at, excluded.last_effective_at), process_epoch = excluded.process_epoch`, wall, service.processEpoch)
	if err != nil {
		return 0, err
	}
	var effective int64
	err = tx.QueryRowContext(ctx, "SELECT last_effective_at FROM admission_clock WHERE singleton = 1").Scan(&effective)
	return effective, err
}

func periodBounds(policy runtimePolicy, millis int64) (int64, *int64) {
	if policy.Algorithm == "fixed_window" {
		window := policy.WindowSeconds * 1000
		start, end := millis-millis%window, millis-millis%window+window
		return start, &end
	}
	if policy.Period == "lifetime" {
		return 0, nil
	}
	t := time.UnixMilli(millis).UTC()
	var start, end time.Time
	switch policy.Period {
	case "hour":
		start = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
		end = start.Add(time.Hour)
	case "day":
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 0, 1)
	case "week":
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
		end = start.AddDate(0, 0, 7)
	case "month":
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
	}
	endMillis := end.UnixMilli()
	return start.UnixMilli(), &endMillis
}

func appendOutbox(ctx context.Context, tx *sql.Tx, eventType, requestID, attemptID string, payload map[string]any, now int64) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO event_outbox (id, event_type, request_id, attempt_id, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)", "evt_"+id, eventType, nullableString(requestID), nullableString(attemptID), string(encoded), now)
	return err
}

func requireOutboxCapacity(ctx context.Context, tx *sql.Tx, additional int64) error {
	var pendingCount, pendingBytes, activeAttempts int64
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM event_outbox WHERE delivered_at IS NULL),
		(SELECT COALESCE(SUM(length(payload_json)), 0) FROM event_outbox WHERE delivered_at IS NULL),
		(SELECT COUNT(*) FROM attempts WHERE state IN ('reserved', 'dispatching', 'streaming'))`).Scan(&pendingCount, &pendingBytes, &activeAttempts); err != nil {
		return err
	}
	reserved := activeAttempts + additional
	if pendingCount+reserved > maxPendingOutboxEvents || pendingBytes+reserved*outboxEventAllowance > maxPendingOutboxBytes {
		return errors.New("usage event outbox is full")
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func checkedAdd(values ...int64) (int64, bool) {
	total := int64(0)
	for _, value := range values {
		if value > 0 && total > math.MaxInt64-value {
			return 0, false
		}
		if value < 0 && total < math.MinInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func saturatingAdd(left, right int64) int64 {
	if value, ok := checkedAdd(left, right); ok {
		return value
	}
	return math.MaxInt64
}

func refillAmount(elapsed, refill, interval, remainder int64) (int64, int64, error) {
	if elapsed < 0 || refill < 0 || interval <= 0 || remainder < 0 || remainder >= interval {
		return 0, 0, errors.New("invalid rate calculation")
	}
	hi, lo := bits.Mul64(uint64(elapsed), uint64(refill))
	lo, carry := bits.Add64(lo, uint64(remainder), 0)
	hi += carry
	if hi >= uint64(interval) {
		return math.MaxInt64, 0, nil
	}
	quotient, next := bits.Div64(hi, lo, uint64(interval))
	if quotient > math.MaxInt64 {
		return math.MaxInt64, int64(next), nil
	}
	return int64(quotient), int64(next), nil
}

func retryAfter(deficit, refill, intervalMillis int64) *int64 {
	if refill <= 0 {
		return nil
	}
	hi, lo := bits.Mul64(uint64(deficit), uint64(intervalMillis))
	if hi >= uint64(refill) {
		return nil
	}
	millis, remainder := bits.Div64(hi, lo, uint64(refill))
	if remainder != 0 {
		millis++
	}
	if millis > math.MaxInt64 {
		return nil
	}
	milliseconds := int64(millis)
	seconds := max(int64(1), (milliseconds-1)/1000+1)
	return &seconds
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (service *Service) MarkDispatching(ctx context.Context, attemptID string) error {
	result, err := service.database.ExecContext(ctx, "UPDATE attempts SET state = 'dispatching' WHERE id = ? AND state = 'reserved'", attemptID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

func (service *Service) debugCounters(ctx context.Context, policyID string) (consumed, reserved int64, err error) {
	err = service.database.QueryRowContext(ctx, "SELECT COALESCE(SUM(consumed_units), 0), COALESCE(SUM(reserved_units), 0) FROM quota_periods WHERE policy_id = ?", policyID).Scan(&consumed, &reserved)
	return
}
