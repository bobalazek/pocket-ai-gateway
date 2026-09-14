package keys

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

var (
	ErrNotFound = errors.New("api key not found")
	ErrConflict = errors.New("api key changed")
	ErrDenied   = errors.New("key grants exceed the user grants")
	ErrExpired  = errors.New("api key expired")
)

var allowedScopes = map[string]bool{
	"chat:generate": true, "responses:generate": true, "embeddings:generate": true,
	"models:read": true, "tokens:count": true,
}

type Service struct{ database *sql.DB }

type APIKey struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	State         string   `json:"state"`
	Scopes        []string `json:"scopes"`
	ModelPatterns []string `json:"model_patterns"`
	ConnectionIDs []string `json:"connection_ids"`
	ExpiresAt     *string  `json:"expires_at"`
	Revision      int64    `json:"revision"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type Input struct {
	Label         string   `json:"label"`
	Scopes        []string `json:"scopes"`
	ModelPatterns []string `json:"model_patterns"`
	ConnectionIDs []string `json:"connection_ids"`
	ExpiresAt     string   `json:"expires_at"`
	State         string   `json:"state,omitempty"`
}

type Principal struct {
	KeyID         string
	OwnerUserID   string
	Scopes        []string
	ModelPatterns []string
	ConnectionIDs []string
}

func (principal Principal) Allows(scope, model, connectionID string) bool {
	if !contains(principal.Scopes, scope) || !contains(principal.ConnectionIDs, connectionID) {
		return false
	}
	for _, pattern := range principal.ModelPatterns {
		if matched, _ := path.Match(pattern, model); matched {
			return true
		}
	}
	return false
}

func New(database *sql.DB) *Service { return &Service{database: database} }

func (service *Service) List(ctx context.Context, ownerID string, limit int, cursor string) ([]APIKey, string, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	before, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	rows, err := service.database.QueryContext(ctx, `SELECT id, label, state, scopes_json, model_patterns_json, connection_ids_json, expires_at, revision, created_at, updated_at
		FROM api_keys WHERE owner_user_id = ? AND (? = 0 OR created_at < ? OR (created_at = ? AND id < ?))
		ORDER BY created_at DESC, id DESC LIMIT ?`, ownerID, before, before, before, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]APIKey, 0, limit)
	var lastCreated int64
	for rows.Next() {
		item, createdAt, err := scanKey(rows)
		if err != nil {
			return nil, "", err
		}
		if len(items) == limit {
			return items, encodeCursor(lastCreated, items[len(items)-1].ID), nil
		}
		lastCreated = createdAt
		items = append(items, item)
	}
	return items, "", rows.Err()
}

func (service *Service) Get(ctx context.Context, ownerID, keyID string) (APIKey, error) {
	row := service.database.QueryRowContext(ctx, `SELECT id, label, state, scopes_json, model_patterns_json, connection_ids_json, expires_at, revision, created_at, updated_at FROM api_keys WHERE id = ? AND owner_user_id = ?`, keyID, ownerID)
	item, _, err := scanKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	return item, err
}

func (service *Service) Create(ctx context.Context, ownerID string, input Input) (APIKey, string, error) {
	input.State = "active"
	validated, expiresAt, err := validateInput(input)
	if err != nil {
		return APIKey{}, "", err
	}
	idPart, err := credentials.RandomToken(16)
	if err != nil {
		return APIKey{}, "", err
	}
	keyID := "key_" + idPart
	secret, selector, verifier, err := newSecret()
	if err != nil {
		return APIKey{}, "", err
	}
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	if allowed, err := service.allowedForUser(ctx, tx, ownerID, validated); err != nil {
		return APIKey{}, "", err
	} else if !allowed {
		return APIKey{}, "", ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys (id, owner_user_id, label, state, scopes_json, model_patterns_json, connection_ids_json, expires_at, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`, keyID, ownerID, validated.Label, encode(validated.Scopes), encode(validated.ModelPatterns), encode(validated.ConnectionIDs), expiresAt, now, now)
	if err != nil {
		return APIKey{}, "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO api_key_secrets (selector, verifier, key_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", selector, verifier, keyID, now, expiresAt); err != nil {
		return APIKey{}, "", err
	}
	if err := audit(ctx, tx, ownerID, "api_key.create", keyID); err != nil {
		return APIKey{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, "", err
	}
	item, err := service.Get(ctx, ownerID, keyID)
	return item, secret, err
}

func (service *Service) Update(ctx context.Context, ownerID, keyID string, revision int64, input Input) (APIKey, error) {
	if _, err := service.Get(ctx, ownerID, keyID); err != nil {
		return APIKey{}, err
	}
	if input.State == "" {
		input.State = "active"
	}
	validated, expiresAt, err := validateInput(input)
	if err != nil {
		return APIKey{}, err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, err
	}
	defer tx.Rollback()
	if allowed, err := service.allowedForUser(ctx, tx, ownerID, validated); err != nil {
		return APIKey{}, err
	} else if !allowed {
		return APIKey{}, ErrDenied
	}
	result, err := tx.ExecContext(ctx, `UPDATE api_keys SET label = ?, state = ?, scopes_json = ?, model_patterns_json = ?, connection_ids_json = ?, expires_at = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND owner_user_id = ? AND revision = ? AND state <> 'revoked'`, validated.Label, validated.State, encode(validated.Scopes), encode(validated.ModelPatterns), encode(validated.ConnectionIDs), expiresAt, time.Now().UnixMilli(), keyID, ownerID, revision)
	if err != nil {
		return APIKey{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return APIKey{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE api_key_secrets SET expires_at = ? WHERE key_id = ? AND revoked_at IS NULL", expiresAt, keyID); err != nil {
		return APIKey{}, err
	}
	if err := audit(ctx, tx, ownerID, "api_key.update", keyID); err != nil {
		return APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, err
	}
	return service.Get(ctx, ownerID, keyID)
}

func (service *Service) Revoke(ctx context.Context, ownerID, keyID string, revision int64) error {
	if _, err := service.Get(ctx, ownerID, keyID); err != nil {
		return err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, "UPDATE api_keys SET state = 'revoked', revision = revision + 1, updated_at = ? WHERE id = ? AND owner_user_id = ? AND revision = ? AND state <> 'revoked'", now, keyID, ownerID, revision)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE api_key_secrets SET revoked_at = ? WHERE key_id = ? AND revoked_at IS NULL", now, keyID); err != nil {
		return err
	}
	if err := audit(ctx, tx, ownerID, "api_key.revoke", keyID); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) Rotate(ctx context.Context, ownerID, keyID string, revision int64) (APIKey, string, error) {
	if _, err := service.Get(ctx, ownerID, keyID); err != nil {
		return APIKey{}, "", err
	}
	secret, selector, verifier, err := newSecret()
	if err != nil {
		return APIKey{}, "", err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	var expiresAt sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT expires_at FROM api_keys WHERE id = ? AND owner_user_id = ? AND revision = ? AND state = 'active'", keyID, ownerID, revision).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, "", ErrConflict
	}
	if err != nil {
		return APIKey{}, "", err
	}
	if expiresAt.Valid && expiresAt.Int64 <= now {
		return APIKey{}, "", ErrExpired
	}
	if _, err := tx.ExecContext(ctx, "UPDATE api_key_secrets SET revoked_at = ? WHERE key_id = ? AND revoked_at IS NULL", now, keyID); err != nil {
		return APIKey{}, "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO api_key_secrets (selector, verifier, key_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", selector, verifier, keyID, now, nullable(expiresAt)); err != nil {
		return APIKey{}, "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE api_keys SET revision = revision + 1, updated_at = ? WHERE id = ?", now, keyID); err != nil {
		return APIKey{}, "", err
	}
	if err := audit(ctx, tx, ownerID, "api_key.rotate", keyID); err != nil {
		return APIKey{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, "", err
	}
	item, err := service.Get(ctx, ownerID, keyID)
	return item, secret, err
}

func (service *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] != "pag" {
		return Principal{}, ErrNotFound
	}
	verifier := credentials.Verifier(token)
	var principal Principal
	var scopesJSON, modelsJSON, connectionsJSON string
	var userScopesJSON, userModelsJSON, userConnectionsJSON string
	var unrestricted bool
	var expected []byte
	err := service.database.QueryRowContext(ctx, `SELECT api_keys.id, api_keys.owner_user_id, api_keys.scopes_json, api_keys.model_patterns_json, api_keys.connection_ids_json, api_key_secrets.verifier,
		       users.inference_unrestricted, users.scopes_json, users.model_patterns_json, users.connection_ids_json
		FROM api_key_secrets JOIN api_keys ON api_keys.id = api_key_secrets.key_id JOIN users ON users.id = api_keys.owner_user_id
		WHERE api_key_secrets.selector = ? AND api_key_secrets.revoked_at IS NULL AND (api_key_secrets.expires_at IS NULL OR api_key_secrets.expires_at > ?)
		  AND api_keys.state = 'active' AND (api_keys.expires_at IS NULL OR api_keys.expires_at > ?) AND users.status = 'active'`, parts[1], time.Now().UnixMilli(), time.Now().UnixMilli()).
		Scan(&principal.KeyID, &principal.OwnerUserID, &scopesJSON, &modelsJSON, &connectionsJSON, &expected, &unrestricted, &userScopesJSON, &userModelsJSON, &userConnectionsJSON)
	if err != nil {
		return Principal{}, ErrNotFound
	}
	if subtle.ConstantTimeCompare(verifier[:], expected) != 1 {
		return Principal{}, ErrNotFound
	}
	if err := json.Unmarshal([]byte(scopesJSON), &principal.Scopes); err != nil {
		return Principal{}, err
	}
	if err := json.Unmarshal([]byte(modelsJSON), &principal.ModelPatterns); err != nil {
		return Principal{}, err
	}
	if err := json.Unmarshal([]byte(connectionsJSON), &principal.ConnectionIDs); err != nil {
		return Principal{}, err
	}
	if !unrestricted {
		var userScopes, userModels, userConnections []string
		_ = json.Unmarshal([]byte(userScopesJSON), &userScopes)
		if err := json.Unmarshal([]byte(userModelsJSON), &userModels); err != nil {
			return Principal{}, err
		}
		if err := json.Unmarshal([]byte(userConnectionsJSON), &userConnections); err != nil {
			return Principal{}, err
		}
		principal.Scopes = intersection(principal.Scopes, userScopes)
		principal.ModelPatterns = modelIntersection(principal.ModelPatterns, userModels)
		principal.ConnectionIDs = intersection(principal.ConnectionIDs, userConnections)
	}
	return principal, nil
}

type queryRow interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (service *Service) allowedForUser(ctx context.Context, query queryRow, userID string, input Input) (bool, error) {
	var unrestricted bool
	var scopesJSON, modelsJSON, connectionsJSON string
	if err := query.QueryRowContext(ctx, "SELECT inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json FROM users WHERE id = ? AND status = 'active'", userID).Scan(&unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON); err != nil {
		return false, err
	}
	if unrestricted {
		return true, nil
	}
	var scopes, models, connections []string
	if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(connectionsJSON), &connections); err != nil {
		return false, err
	}
	return subset(input.Scopes, scopes) && modelSubset(input.ModelPatterns, models) && subset(input.ConnectionIDs, connections), nil
}

type scanner interface{ Scan(...any) error }

func scanKey(row scanner) (APIKey, int64, error) {
	var item APIKey
	var scopesJSON, modelsJSON, connectionsJSON string
	var expires sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(&item.ID, &item.Label, &item.State, &scopesJSON, &modelsJSON, &connectionsJSON, &expires, &item.Revision, &createdAt, &updatedAt)
	if err != nil {
		return APIKey{}, 0, err
	}
	if err := json.Unmarshal([]byte(scopesJSON), &item.Scopes); err != nil {
		return APIKey{}, 0, err
	}
	_ = json.Unmarshal([]byte(modelsJSON), &item.ModelPatterns)
	_ = json.Unmarshal([]byte(connectionsJSON), &item.ConnectionIDs)
	if expires.Valid {
		value := formatTime(expires.Int64)
		item.ExpiresAt = &value
	}
	item.CreatedAt, item.UpdatedAt = formatTime(createdAt), formatTime(updatedAt)
	return item, createdAt, nil
}

func validateInput(input Input) (Input, any, error) {
	input.Label = strings.TrimSpace(input.Label)
	if input.Label == "" || len([]rune(input.Label)) > 100 {
		return Input{}, nil, fmt.Errorf("label must be between 1 and 100 characters")
	}
	if input.State != "active" && input.State != "disabled" {
		return Input{}, nil, fmt.Errorf("state must be active or disabled")
	}
	for _, scope := range input.Scopes {
		if !allowedScopes[scope] {
			return Input{}, nil, fmt.Errorf("unsupported scope %q", scope)
		}
	}
	if err := validateGrants(input.ModelPatterns); err != nil {
		return Input{}, nil, err
	}
	if err := validateGrants(input.ConnectionIDs); err != nil {
		return Input{}, nil, err
	}
	input.Scopes = unique(input.Scopes)
	input.ModelPatterns = unique(input.ModelPatterns)
	input.ConnectionIDs = unique(input.ConnectionIDs)
	var expiresAt any
	if input.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, input.ExpiresAt)
		if err != nil || !parsed.After(time.Now()) {
			return Input{}, nil, fmt.Errorf("expiry must be a future RFC3339 time")
		}
		expiresAt = parsed.UnixMilli()
	}
	return input, expiresAt, nil
}

func validateGrants(values []string) error {
	if len(values) > 100 {
		return errors.New("too many grants")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 200 {
			return errors.New("grants must be between 1 and 200 characters")
		}
	}
	return nil
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func subset(values, ceiling []string) bool {
	return len(intersection(values, ceiling)) == len(unique(values))
}

func intersection(values, ceiling []string) []string {
	allowed := map[string]bool{}
	for _, value := range ceiling {
		allowed[value] = true
	}
	result := []string{}
	for _, value := range unique(values) {
		if allowed[value] {
			result = append(result, value)
		}
	}
	return result
}

func modelSubset(values, ceiling []string) bool {
	return len(modelIntersection(values, ceiling)) == len(unique(values))
}

func modelIntersection(values, ceiling []string) []string {
	result := []string{}
	for _, value := range unique(values) {
		for _, allowed := range ceiling {
			matched, err := path.Match(allowed, value)
			allowedPrefix, allowedIsPrefix := simplePrefixPattern(allowed)
			valuePrefix, valueIsPrefix := simplePrefixPattern(value)
			if value == allowed || (!strings.ContainsAny(value, "*?[") && err == nil && matched) || (allowedIsPrefix && valueIsPrefix && strings.HasPrefix(valuePrefix, allowedPrefix) && strings.Count(valuePrefix, "/") == strings.Count(allowedPrefix, "/") && !strings.ContainsAny(valuePrefix+allowedPrefix, `\`)) {
				result = append(result, value)
				break
			}
		}
	}
	return result
}

func simplePrefixPattern(pattern string) (string, bool) {
	if strings.Count(pattern, "*") != 1 || !strings.HasSuffix(pattern, "*") || strings.ContainsAny(strings.TrimSuffix(pattern, "*"), "?[") {
		return "", false
	}
	return strings.TrimSuffix(pattern, "*"), true
}

func newSecret() (token, selector string, verifier []byte, err error) {
	selectorSeed, err := credentials.RandomToken(8)
	if err != nil {
		return "", "", nil, err
	}
	selectorHash := credentials.Verifier(selectorSeed)
	selector = hex.EncodeToString(selectorHash[:8])
	secret, err := credentials.RandomToken(32)
	if err != nil {
		return "", "", nil, err
	}
	token = "pag_" + selector + "_" + secret
	sum := credentials.Verifier(token)
	return token, selector, sum[:], nil
}

func encode(value []string) string {
	if value == nil {
		value = []string{}
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func audit(ctx context.Context, tx *sql.Tx, actorID, action, keyID string) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (id, actor_user_id, action, resource_type, resource_id, created_at) VALUES (?, ?, ?, 'api_key', ?, ?)", "aud_"+id, actorID, action, keyID, time.Now().UnixMilli())
	return err
}

func nullable(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func encodeCursor(createdAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt, 10) + ":" + id))
}

func decodeCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", err
	}
	timePart, id, ok := strings.Cut(string(decoded), ":")
	createdAt, err := strconv.ParseInt(timePart, 10, 64)
	if err != nil || !ok || id == "" {
		return 0, "", errors.New("invalid cursor")
	}
	return createdAt, id, nil
}

func formatTime(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format(time.RFC3339)
}
