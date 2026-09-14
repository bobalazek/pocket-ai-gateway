package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

var (
	ErrNotFound = errors.New("usage resource not found")
	ErrDenied   = errors.New("usage operation is not permitted")
	ErrConflict = errors.New("usage resource changed")
)

const maxSafeInteger = int64(9_007_199_254_740_991)

type Service struct {
	database     *sql.DB
	processEpoch string
	now          func() time.Time
}

func New(database *sql.DB) *Service {
	epoch, _ := credentials.RandomToken(12)
	return &Service{database: database, processEpoch: "ep_" + epoch, now: time.Now}
}

type Policy struct {
	ID               string `json:"id"`
	ScopeKind        string `json:"scope_kind"`
	ScopeID          string `json:"scope_id"`
	Metric           string `json:"metric"`
	Algorithm        string `json:"algorithm"`
	Period           string `json:"period"`
	WindowSeconds    int64  `json:"window_seconds"`
	LimitUnits       int64  `json:"limit_units"`
	LimitUSD         string `json:"limit_usd,omitempty"`
	RefillUnits      int64  `json:"refill_units"`
	RefillIntervalMS int64  `json:"refill_interval_ms"`
	Enabled          bool   `json:"enabled"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type PolicyInput struct {
	ScopeKind        string `json:"scope_kind"`
	ScopeID          string `json:"scope_id"`
	Metric           string `json:"metric"`
	Algorithm        string `json:"algorithm"`
	Period           string `json:"period"`
	WindowSeconds    int64  `json:"window_seconds"`
	LimitUnits       int64  `json:"limit_units"`
	LimitUSD         string `json:"limit_usd"`
	RefillUnits      int64  `json:"refill_units"`
	RefillIntervalMS int64  `json:"refill_interval_ms"`
	Enabled          *bool  `json:"enabled,omitempty"`
}

func (service *Service) CreatePolicy(ctx context.Context, actor auth.User, input PolicyInput) (Policy, error) {
	input, err := validatePolicyInput(input)
	if err != nil {
		return Policy{}, err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return Policy{}, err
	}
	if err := authorizePolicyScope(ctx, tx, actor, input.ScopeKind, input.ScopeID); err != nil {
		return Policy{}, err
	}
	id, err := credentials.RandomToken(16)
	if err != nil {
		return Policy{}, err
	}
	now := service.now().UnixMilli()
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO limit_policies
		(id, scope_kind, scope_id, metric, algorithm, period, window_seconds, limit_units, refill_units, refill_interval_ms, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"pol_"+id, input.ScopeKind, input.ScopeID, input.Metric, input.Algorithm, input.Period, input.WindowSeconds, input.LimitUnits, input.RefillUnits, input.RefillIntervalMS, enabled, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Policy{}, ErrConflict
		}
		return Policy{}, err
	}
	if err := insertAudit(ctx, tx, actor.ID, "policy.create", "policy", "pol_"+id, map[string]any{"scope_kind": input.ScopeKind, "scope_id": input.ScopeID, "metric": input.Metric}); err != nil {
		return Policy{}, err
	}
	if err := tx.Commit(); err != nil {
		return Policy{}, err
	}
	return service.GetPolicy(ctx, actor, "pol_"+id)
}

func (service *Service) UpdatePolicy(ctx context.Context, actor auth.User, id string, revision int64, limitUnits int64, limitUSD string, enabled bool) (Policy, error) {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	actor, err = refreshUsageActor(ctx, tx, actor)
	if err != nil {
		return Policy{}, err
	}
	policy, err := readPolicy(ctx, tx, id)
	if err != nil {
		return Policy{}, err
	}
	if err := authorizePolicyScope(ctx, tx, actor, policy.ScopeKind, policy.ScopeID); err != nil {
		return Policy{}, err
	}
	if policy.Metric == "spend" {
		limitUnits, err = ParseUSD(limitUSD)
	}
	if err != nil || limitUnits <= 0 || limitUnits > maxSafeInteger {
		return Policy{}, errors.New("limit must be between 1 and 9007199254740991")
	}
	result, err := tx.ExecContext(ctx, "UPDATE limit_policies SET limit_units = ?, enabled = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?", limitUnits, enabled, service.now().UnixMilli(), id, revision)
	if err != nil {
		return Policy{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Policy{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, actor.ID, "policy.update", "policy", id, map[string]any{"limit_units": limitUnits, "enabled": enabled}); err != nil {
		return Policy{}, err
	}
	if err := tx.Commit(); err != nil {
		return Policy{}, err
	}
	return service.GetPolicy(ctx, actor, id)
}

func (service *Service) GetPolicy(ctx context.Context, actor auth.User, id string) (Policy, error) {
	actor, err := refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return Policy{}, err
	}
	policy, err := readPolicy(ctx, service.database, id)
	if err != nil {
		return Policy{}, err
	}
	if err := authorizePolicyScope(ctx, service.database, actor, policy.ScopeKind, policy.ScopeID); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (service *Service) ListPolicies(ctx context.Context, actor auth.User, cursor string) ([]Policy, string, error) {
	actor, err := refreshUsageActor(ctx, service.database, actor)
	if err != nil {
		return nil, "", err
	}
	before, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT id, scope_kind, scope_id, metric, algorithm, period, window_seconds, limit_units, refill_units, refill_interval_ms, enabled, revision, created_at, updated_at FROM limit_policies`
	args := []any{}
	conditions := []string{}
	if actor.Role == "member" {
		conditions = append(conditions, `((scope_kind = 'user' AND scope_id = ?) OR (scope_kind = 'key' AND scope_id IN (SELECT id FROM api_keys WHERE owner_user_id = ?)))`)
		args = append(args, actor.ID, actor.ID)
	} else if actor.Role == "admin" {
		conditions = append(conditions, `(scope_kind IN ('instance', 'connection') OR (scope_kind = 'user' AND scope_id IN (SELECT id FROM users WHERE role = 'member')) OR (scope_kind = 'key' AND scope_id IN (SELECT api_keys.id FROM api_keys JOIN users ON users.id = api_keys.owner_user_id WHERE users.role = 'member')))`)
	}
	if cursor != "" {
		conditions = append(conditions, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, before, before, beforeID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT 51"
	rows, err := service.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Policy{}
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) <= 50 {
		return items, "", nil
	}
	items = items[:50]
	created, _ := time.Parse(time.RFC3339Nano, items[len(items)-1].CreatedAt)
	return items, encodeCursor(created.UnixMilli(), items[len(items)-1].ID), nil
}

func refreshUsageActor(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, actor auth.User) (auth.User, error) {
	err := queryer.QueryRowContext(ctx, "SELECT email, display_name, role, status FROM users WHERE id = ? AND status = 'active'", actor.ID).
		Scan(&actor.Email, &actor.DisplayName, &actor.Role, &actor.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, ErrDenied
	}
	return actor, err
}

type scanner interface{ Scan(...any) error }

func readPolicy(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Policy, error) {
	policy, err := scanPolicy(queryer.QueryRowContext(ctx, `SELECT id, scope_kind, scope_id, metric, algorithm, period, window_seconds, limit_units, refill_units, refill_interval_ms, enabled, revision, created_at, updated_at FROM limit_policies WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	return policy, err
}

func scanPolicy(row scanner) (Policy, error) {
	var policy Policy
	var enabled bool
	var createdAt, updatedAt int64
	err := row.Scan(&policy.ID, &policy.ScopeKind, &policy.ScopeID, &policy.Metric, &policy.Algorithm, &policy.Period, &policy.WindowSeconds, &policy.LimitUnits, &policy.RefillUnits, &policy.RefillIntervalMS, &enabled, &policy.Revision, &createdAt, &updatedAt)
	policy.Enabled = enabled
	policy.CreatedAt = time.UnixMilli(createdAt).UTC().Format(time.RFC3339Nano)
	policy.UpdatedAt = time.UnixMilli(updatedAt).UTC().Format(time.RFC3339Nano)
	if policy.Metric == "spend" {
		policy.LimitUSD = FormatUSD(policy.LimitUnits)
	}
	return policy, err
}

func validatePolicyInput(input PolicyInput) (PolicyInput, error) {
	input.ScopeKind = strings.TrimSpace(input.ScopeKind)
	input.ScopeID = strings.TrimSpace(input.ScopeID)
	input.Metric = strings.TrimSpace(input.Metric)
	input.Algorithm = strings.TrimSpace(input.Algorithm)
	input.Period = strings.TrimSpace(input.Period)
	if input.WindowSeconds > maxSafeInteger || input.RefillUnits > maxSafeInteger || input.RefillIntervalMS > maxSafeInteger {
		return input, errors.New("policy values must not exceed 9007199254740991")
	}
	if input.ScopeKind == "instance" {
		input.ScopeID = ""
	} else if input.ScopeID == "" {
		return input, errors.New("scope_id is required")
	}
	if input.Metric == "spend" {
		limit, err := ParseUSD(input.LimitUSD)
		if err != nil {
			return input, err
		}
		input.LimitUnits = limit
	}
	if input.LimitUnits <= 0 || input.LimitUnits > maxSafeInteger {
		return input, errors.New("limit must be between 1 and 9007199254740991")
	}
	validScope := input.ScopeKind == "instance" || input.ScopeKind == "user" || input.ScopeKind == "key" || input.ScopeKind == "connection"
	validMetric := contains([]string{"requests", "tokens", "spend", "concurrency", "body_bytes", "output_tokens", "batch_items"}, input.Metric)
	if !validScope || !validMetric {
		return input, errors.New("unsupported policy scope or metric")
	}
	switch input.Algorithm {
	case "token_bucket":
		if input.Period != "" || input.WindowSeconds != 0 || input.RefillUnits <= 0 || input.RefillIntervalMS <= 0 || (input.Metric != "requests" && input.Metric != "tokens") {
			return input, errors.New("invalid token-bucket policy")
		}
	case "fixed_window":
		if input.Period != "" || input.WindowSeconds <= 0 || input.WindowSeconds > mathMaxInt64/1000 || input.RefillUnits != 0 || input.RefillIntervalMS != 0 || (input.Metric != "requests" && input.Metric != "tokens") {
			return input, errors.New("invalid fixed-window policy")
		}
	case "quota":
		if !contains([]string{"hour", "day", "week", "month", "lifetime"}, input.Period) || input.WindowSeconds != 0 || input.RefillUnits != 0 || input.RefillIntervalMS != 0 || !contains([]string{"requests", "tokens", "spend"}, input.Metric) {
			return input, errors.New("invalid quota policy")
		}
	case "concurrency":
		if input.Metric != "concurrency" || input.Period != "" || input.WindowSeconds != 0 || input.RefillUnits != 0 || input.RefillIntervalMS != 0 {
			return input, errors.New("invalid concurrency policy")
		}
	case "ceiling":
		if !contains([]string{"body_bytes", "output_tokens", "batch_items"}, input.Metric) || input.Period != "" || input.WindowSeconds != 0 || input.RefillUnits != 0 || input.RefillIntervalMS != 0 {
			return input, errors.New("invalid ceiling policy")
		}
	default:
		return input, errors.New("unsupported policy algorithm")
	}
	return input, nil
}

func authorizePolicyScope(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, actor auth.User, scopeKind, scopeID string) error {
	if scopeKind == "instance" {
		if actor.Role == "owner" {
			return nil
		}
		return ErrDenied
	}
	if scopeKind == "connection" {
		if actor.Role == "owner" || actor.Role == "admin" {
			return nil
		}
		return ErrDenied
	}
	var role string
	var err error
	if scopeKind == "user" {
		err = queryer.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ? AND status <> 'archived'", scopeID).Scan(&role)
	} else {
		err = queryer.QueryRowContext(ctx, "SELECT users.role FROM api_keys JOIN users ON users.id = api_keys.owner_user_id WHERE api_keys.id = ?", scopeID).Scan(&role)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if actor.Role == "owner" {
		return nil
	}
	if actor.Role != "admin" {
		return ErrDenied
	}
	if role != "member" {
		return ErrDenied
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func insertAudit(ctx context.Context, tx *sql.Tx, actorID, action, resourceType, resourceID string, detail map[string]any) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (id, actor_user_id, action, resource_type, resource_id, detail_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", "aud_"+id, actorID, action, resourceType, resourceID, string(encoded), time.Now().UnixMilli())
	return err
}
