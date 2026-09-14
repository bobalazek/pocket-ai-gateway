package users

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

const codeLifetime = 24 * time.Hour

var allowedScopes = map[string]bool{
	"chat:generate": true, "responses:generate": true, "embeddings:generate": true,
	"models:read": true, "tokens:count": true,
}

var (
	ErrDenied   = errors.New("operation denied")
	ErrNotFound = errors.New("user not found")
	ErrConflict = errors.New("resource changed")
)

type Service struct{ database *sql.DB }

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Grants      Grants `json:"grants"`
}

type Grants struct {
	Unrestricted  bool     `json:"unrestricted"`
	Scopes        []string `json:"scopes"`
	ModelPatterns []string `json:"model_patterns"`
	ConnectionIDs []string `json:"connection_ids"`
}

type CreateInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Grants      Grants `json:"grants"`
}

type UpdateInput struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Status      *string `json:"status"`
}

func New(database *sql.DB) *Service { return &Service{database: database} }

func (service *Service) List(ctx context.Context, actor auth.User, limit int, cursor string) ([]User, string, error) {
	actor, err := refreshActor(ctx, service.database, actor.ID)
	if err != nil {
		return nil, "", err
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		return nil, "", ErrDenied
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	beforeTime, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", &auth.InputError{Message: "Cursor is invalid"}
	}
	roleClause := ""
	if actor.Role == "admin" {
		roleClause = "AND role = 'member'"
	}
	rows, err := service.database.QueryContext(ctx, `
		SELECT id, email, display_name, role,
		       CASE WHEN status = 'suspended' AND EXISTS(SELECT 1 FROM activation_tokens WHERE user_id = users.id AND purpose = 'activation') THEN 'pending_activation' ELSE status END,
		       revision, created_at, updated_at, inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json
		FROM users WHERE (? = 0 OR created_at < ? OR (created_at = ? AND id < ?)) `+roleClause+`
		ORDER BY created_at DESC, id DESC LIMIT ?`, beforeTime, beforeTime, beforeTime, beforeID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	items := make([]User, 0, limit)
	var lastCreated int64
	for rows.Next() {
		var item User
		var createdAt, updatedAt int64
		var scopesJSON, modelsJSON, connectionsJSON string
		if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName, &item.Role, &item.Status, &item.Revision, &createdAt, &updatedAt, &item.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON); err != nil {
			return nil, "", fmt.Errorf("scan user: %w", err)
		}
		if err := decodeGrants(&item.Grants, scopesJSON, modelsJSON, connectionsJSON); err != nil {
			return nil, "", err
		}
		if len(items) == limit {
			return items, encodeCursor(lastCreated, items[len(items)-1].ID), nil
		}
		lastCreated = createdAt
		item.CreatedAt, item.UpdatedAt = formatTime(createdAt), formatTime(updatedAt)
		items = append(items, item)
	}
	return items, "", rows.Err()
}

func (service *Service) Create(ctx context.Context, actor auth.User, input CreateInput) (User, string, error) {
	email, displayName, err := validateIdentity(input.Email, input.DisplayName)
	if err != nil {
		return User{}, "", err
	}
	input.Grants.Unrestricted = false
	if err := validateGrants(input.Grants); err != nil {
		return User{}, "", err
	}
	idPart, err := credentials.RandomToken(16)
	if err != nil {
		return User{}, "", err
	}
	code, err := credentials.RandomToken(24)
	if err != nil {
		return User{}, "", err
	}
	user := User{ID: "usr_" + idPart, Email: email, DisplayName: displayName, Role: input.Role, Status: "pending_activation", Revision: 1, Grants: input.Grants}
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("begin user creation: %w", err)
	}
	defer tx.Rollback()
	actor, err = refreshActor(ctx, tx, actor.ID)
	if err != nil {
		return User{}, "", err
	}
	if (actor.Role == "owner" && input.Role != "admin" && input.Role != "member") || (actor.Role == "admin" && input.Role != "member") || (actor.Role != "owner" && actor.Role != "admin") {
		return User{}, "", ErrDenied
	}
	if allowed, err := service.canGrant(ctx, tx, actor.ID, input.Grants); err != nil {
		return User{}, "", err
	} else if !allowed {
		return User{}, "", ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users (id, email, display_name, password_hash, role, status, scopes_json, model_patterns_json, connection_ids_json, created_at, updated_at) VALUES (?, ?, ?, '!', ?, 'suspended', ?, ?, ?, ?, ?)`, user.ID, email, displayName, input.Role, encode(input.Grants.Scopes), encode(input.Grants.ModelPatterns), encode(input.Grants.ConnectionIDs), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", &auth.InputError{Message: "A user with this email already exists"}
		}
		return User{}, "", fmt.Errorf("create user: %w", err)
	}
	if err := insertCode(ctx, tx, user.ID, "activation", code, now); err != nil {
		return User{}, "", err
	}
	if err := audit(ctx, tx, actor.ID, "user.create", "user", user.ID, map[string]any{"role": input.Role}); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("commit user creation: %w", err)
	}
	user.CreatedAt, user.UpdatedAt = formatTime(now), formatTime(now)
	return user, code, nil
}

func (service *Service) UpdateGrants(ctx context.Context, actor auth.User, targetID string, revision int64, grants Grants) (User, error) {
	grants.Unrestricted = false
	if err := validateGrants(grants); err != nil {
		return User{}, err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	actor, err = refreshActor(ctx, tx, actor.ID)
	if err != nil {
		return User{}, err
	}
	target, _, err := readUser(ctx, tx, targetID)
	if err != nil {
		return User{}, err
	}
	if !canManage(actor, target) || target.Revision != revision {
		if target.Revision != revision {
			return User{}, ErrConflict
		}
		return User{}, ErrDenied
	}
	if allowed, err := service.canGrant(ctx, tx, actor.ID, grants); err != nil {
		return User{}, err
	} else if !allowed {
		return User{}, ErrDenied
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE users SET scopes_json = ?, model_patterns_json = ?, connection_ids_json = ?, revision = revision + 1, auth_revision = auth_revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, encode(grants.Scopes), encode(grants.ModelPatterns), encode(grants.ConnectionIDs), now, targetID, revision)
	if err != nil {
		return User{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return User{}, ErrConflict
	}
	if err := clampKeys(ctx, tx, targetID, grants, now); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", targetID); err != nil {
		return User{}, err
	}
	if err := audit(ctx, tx, actor.ID, "user.grants_update", "user", targetID, map[string]any{"scopes": grants.Scopes, "model_patterns": grants.ModelPatterns, "connection_ids": grants.ConnectionIDs}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return service.Get(ctx, actor, targetID)
}

func (service *Service) Update(ctx context.Context, actor auth.User, targetID string, revision int64, input UpdateInput) (User, error) {
	if targetID == actor.ID {
		return User{}, ErrDenied
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	actor, err = refreshActor(ctx, tx, actor.ID)
	if err != nil {
		return User{}, err
	}
	target, rawStatus, err := readUser(ctx, tx, targetID)
	if err != nil {
		return User{}, err
	}
	if !canManage(actor, target) {
		return User{}, ErrDenied
	}
	if target.Revision != revision {
		return User{}, ErrConflict
	}
	email, displayName := target.Email, target.DisplayName
	if input.Email != nil {
		email, _, err = validateIdentity(*input.Email, displayName)
		if err != nil {
			return User{}, err
		}
	}
	if input.DisplayName != nil {
		_, displayName, err = validateIdentity(email, *input.DisplayName)
		if err != nil {
			return User{}, err
		}
	}
	status := rawStatus
	if input.Status != nil {
		if *input.Status != "active" && *input.Status != "suspended" {
			return User{}, &auth.InputError{Message: "Status must be active or suspended"}
		}
		if target.Status == "pending_activation" && *input.Status == "active" {
			return User{}, &auth.InputError{Message: "Pending users must activate with their code"}
		}
		status = *input.Status
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE users SET email = ?, display_name = ?, status = ?, revision = revision + 1, auth_revision = auth_revision + CASE WHEN status <> ? OR email <> ? THEN 1 ELSE 0 END, updated_at = ? WHERE id = ? AND revision = ?`, email, displayName, status, status, email, now, targetID, revision)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, &auth.InputError{Message: "A user with this email already exists"}
		}
		return User{}, fmt.Errorf("update user: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return User{}, ErrConflict
	}
	if status != rawStatus || email != target.Email {
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", targetID); err != nil {
			return User{}, fmt.Errorf("revoke user sessions: %w", err)
		}
	}
	if input.Status != nil && *input.Status == "suspended" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM activation_tokens WHERE user_id = ?", targetID); err != nil {
			return User{}, err
		}
	}
	if err := audit(ctx, tx, actor.ID, "user.update", "user", targetID, map[string]any{"status": status}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return service.Get(ctx, actor, targetID)
}

func (service *Service) Get(ctx context.Context, actor auth.User, targetID string) (User, error) {
	actor, err := refreshActor(ctx, service.database, actor.ID)
	if err != nil {
		return User{}, err
	}
	target, _, err := readUser(ctx, service.database, targetID)
	if err != nil {
		return User{}, err
	}
	if !canManage(actor, target) {
		return User{}, ErrNotFound
	}
	return target, nil
}

func (service *Service) IssueCode(ctx context.Context, actor auth.User, targetID, purpose string) (string, error) {
	if purpose != "activation" && purpose != "recovery" {
		return "", ErrNotFound
	}
	code, err := credentials.RandomToken(24)
	if err != nil {
		return "", err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	actor, err = refreshActor(ctx, tx, actor.ID)
	if err != nil {
		return "", err
	}
	target, rawStatus, err := readUser(ctx, tx, targetID)
	if err != nil {
		return "", err
	}
	if !canManage(actor, target) {
		return "", ErrDenied
	}
	if purpose == "activation" && target.Status != "pending_activation" {
		return "", &auth.InputError{Message: "User is already activated"}
	}
	if purpose == "recovery" && rawStatus != "active" {
		return "", &auth.InputError{Message: "Reactivate the user before issuing a recovery code"}
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "DELETE FROM activation_tokens WHERE user_id = ?", targetID); err != nil {
		return "", err
	}
	if err := insertCode(ctx, tx, targetID, purpose, code, now); err != nil {
		return "", err
	}
	if purpose == "recovery" {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET auth_revision = auth_revision + 1, revision = revision + 1, updated_at = ? WHERE id = ?", now, targetID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", targetID); err != nil {
			return "", err
		}
	}
	if err := audit(ctx, tx, actor.ID, "user."+purpose+"_code", "user", targetID, nil); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

func (service *Service) TransferOwner(ctx context.Context, actor auth.User, targetID string, targetRevision int64) error {
	if actor.ID == targetID {
		return ErrDenied
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err = refreshActor(ctx, tx, actor.ID)
	if err != nil || actor.Role != "owner" {
		return ErrDenied
	}
	target, rawStatus, err := readUser(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if target.Role != "admin" || rawStatus != "active" || target.Revision != targetRevision {
		return ErrConflict
	}
	now := time.Now().UnixMilli()
	oldOwner, err := tx.ExecContext(ctx, "UPDATE users SET role = 'admin', inference_unrestricted = 0, scopes_json = '[]', model_patterns_json = '[]', connection_ids_json = '[]', revision = revision + 1, auth_revision = auth_revision + 1, updated_at = ? WHERE id = ? AND role = 'owner'", now, actor.ID)
	if err != nil {
		return err
	}
	if changed, _ := oldOwner.RowsAffected(); changed != 1 {
		return ErrDenied
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET role = 'owner', inference_unrestricted = 1, revision = revision + 1, auth_revision = auth_revision + 1, updated_at = ? WHERE id = ? AND role = 'admin' AND status = 'active' AND revision = ?", now, targetID, targetRevision)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id IN (?, ?)", actor.ID, targetID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM activation_tokens WHERE user_id IN (?, ?)", actor.ID, targetID); err != nil {
		return err
	}
	if err := clampKeys(ctx, tx, actor.ID, Grants{}, now); err != nil {
		return err
	}
	if err := audit(ctx, tx, actor.ID, "owner.transfer", "user", targetID, nil); err != nil {
		return err
	}
	return tx.Commit()
}

type queryRow interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func refreshActor(ctx context.Context, query queryRow, id string) (auth.User, error) {
	var actor auth.User
	err := query.QueryRowContext(ctx, "SELECT id, email, display_name, role, status FROM users WHERE id = ? AND status = 'active'", id).
		Scan(&actor.ID, &actor.Email, &actor.DisplayName, &actor.Role, &actor.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, ErrDenied
	}
	if err != nil {
		return auth.User{}, err
	}
	return actor, nil
}

func readUser(ctx context.Context, query queryRow, id string) (User, string, error) {
	var item User
	var status string
	var createdAt, updatedAt int64
	var scopesJSON, modelsJSON, connectionsJSON string
	err := query.QueryRowContext(ctx, `SELECT id, email, display_name, role,
		CASE WHEN status = 'suspended' AND EXISTS(SELECT 1 FROM activation_tokens WHERE user_id = users.id AND purpose = 'activation') THEN 'pending_activation' ELSE status END,
		status, revision, created_at, updated_at, inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json FROM users WHERE id = ?`, id).
		Scan(&item.ID, &item.Email, &item.DisplayName, &item.Role, &item.Status, &status, &item.Revision, &createdAt, &updatedAt, &item.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	if err != nil {
		return User{}, "", err
	}
	item.CreatedAt, item.UpdatedAt = formatTime(createdAt), formatTime(updatedAt)
	if err := decodeGrants(&item.Grants, scopesJSON, modelsJSON, connectionsJSON); err != nil {
		return User{}, "", err
	}
	return item, status, nil
}

func (service *Service) canGrant(ctx context.Context, query queryRow, actorID string, grants Grants) (bool, error) {
	var unrestricted bool
	var scopesJSON, modelsJSON, connectionsJSON string
	if err := query.QueryRowContext(ctx, "SELECT inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json FROM users WHERE id = ? AND status = 'active'", actorID).Scan(&unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON); err != nil {
		return false, err
	}
	if unrestricted {
		return true, nil
	}
	var ceiling Grants
	if err := decodeGrants(&ceiling, scopesJSON, modelsJSON, connectionsJSON); err != nil {
		return false, err
	}
	return subset(grants.Scopes, ceiling.Scopes) && modelSubset(grants.ModelPatterns, ceiling.ModelPatterns) && subset(grants.ConnectionIDs, ceiling.ConnectionIDs), nil
}

func clampKeys(ctx context.Context, tx *sql.Tx, userID string, ceiling Grants, now int64) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, scopes_json, model_patterns_json, connection_ids_json FROM api_keys WHERE owner_user_id = ?", userID)
	if err != nil {
		return err
	}
	type savedKey struct {
		id, scopes, models, connections string
	}
	keys := []savedKey{}
	for rows.Next() {
		var key savedKey
		if err := rows.Scan(&key.id, &key.scopes, &key.models, &key.connections); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, key := range keys {
		var scopes, models, connections []string
		if err := json.Unmarshal([]byte(key.scopes), &scopes); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(key.models), &models); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(key.connections), &connections); err != nil {
			return err
		}
		newScopes := encode(intersection(scopes, ceiling.Scopes))
		newModels := encode(modelIntersection(models, ceiling.ModelPatterns))
		newConnections := encode(intersection(connections, ceiling.ConnectionIDs))
		if newScopes == key.scopes && newModels == key.models && newConnections == key.connections {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE api_keys SET scopes_json = ?, model_patterns_json = ?, connection_ids_json = ?, revision = revision + 1, updated_at = ? WHERE id = ?", newScopes, newModels, newConnections, now, key.id); err != nil {
			return err
		}
	}
	return nil
}

func canManage(actor auth.User, target User) bool {
	return actor.Role == "owner" || (actor.Role == "admin" && target.Role == "member")
}

func validateIdentity(rawEmail, rawName string) (string, string, error) {
	email := strings.ToLower(strings.TrimSpace(rawEmail))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return "", "", &auth.InputError{Message: "Enter a valid email address"}
	}
	name := strings.TrimSpace(rawName)
	if name == "" || len([]rune(name)) > 100 {
		return "", "", &auth.InputError{Message: "Display name must be between 1 and 100 characters"}
	}
	return email, name, nil
}

func insertCode(ctx context.Context, tx *sql.Tx, userID, purpose, code string, now int64) error {
	verifier := credentials.Verifier(code)
	_, err := tx.ExecContext(ctx, "INSERT INTO activation_tokens (verifier, user_id, purpose, expires_at, created_at) VALUES (?, ?, ?, ?, ?)", verifier[:], userID, purpose, now+codeLifetime.Milliseconds(), now)
	return err
}

func validateGrants(grants Grants) error {
	for _, scope := range grants.Scopes {
		if !allowedScopes[scope] {
			return &auth.InputError{Message: "Unsupported inference scope: " + scope}
		}
	}
	for _, values := range [][]string{grants.Scopes, grants.ModelPatterns, grants.ConnectionIDs} {
		if len(values) > 100 {
			return &auth.InputError{Message: "No more than 100 grants are allowed per group"}
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 200 {
				return &auth.InputError{Message: "Grants must be between 1 and 200 characters"}
			}
		}
	}
	grants.Scopes = unique(grants.Scopes)
	grants.ModelPatterns = unique(grants.ModelPatterns)
	grants.ConnectionIDs = unique(grants.ConnectionIDs)
	return nil
}

func decodeGrants(grants *Grants, scopesJSON, modelsJSON, connectionsJSON string) error {
	if err := json.Unmarshal([]byte(scopesJSON), &grants.Scopes); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(modelsJSON), &grants.ModelPatterns); err != nil {
		return err
	}
	return json.Unmarshal([]byte(connectionsJSON), &grants.ConnectionIDs)
}

func encode(values []string) string {
	if values == nil {
		values = []string{}
	}
	encoded, _ := json.Marshal(unique(values))
	return string(encoded)
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

func subset(values, ceiling []string) bool {
	allowed := map[string]bool{}
	for _, value := range ceiling {
		allowed[value] = true
	}
	for _, value := range values {
		if !allowed[value] {
			return false
		}
	}
	return true
}

func intersection(values, ceiling []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if subset([]string{value}, ceiling) {
			result = append(result, value)
		}
	}
	return result
}

func modelSubset(values, ceiling []string) bool {
	return len(modelIntersection(values, ceiling)) == len(unique(values))
}

func modelIntersection(values, ceiling []string) []string {
	result := make([]string, 0, len(values))
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

func audit(ctx context.Context, tx *sql.Tx, actorID, action, resourceType, resourceID string, detail any) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	if detail == nil {
		detail = struct{}{}
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (id, actor_user_id, action, resource_type, resource_id, detail_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", "aud_"+id, actorID, action, resourceType, resourceID, string(encoded), time.Now().UnixMilli())
	return err
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
