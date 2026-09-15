package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	sessionLifetime     = 24 * time.Hour
	sessionIdleLifetime = 30 * time.Minute
	sessionTouchAfter   = 5 * time.Minute
)

var (
	ErrSetupComplete  = errors.New("owner setup is already complete")
	ErrInvalidSession = errors.New("session is invalid or expired")
)

type Service struct {
	database *sql.DB
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Grants      Grants `json:"grants"`
}

type Grants struct {
	Unrestricted  bool     `json:"unrestricted"`
	Scopes        []string `json:"scopes"`
	ModelPatterns []string `json:"model_patterns"`
	ConnectionIDs []string `json:"connection_ids"`
}

type AuthenticatedSession struct {
	ID              string
	User            User
	CreatedAt       int64
	LastSeenAt      int64
	ExpiresAt       int64
	AuthenticatedAt int64
	AuthRevision    int64
	UserAgent       string
}

type ClaimInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type InputError struct{ Message string }

func (err *InputError) Error() string { return err.Message }

func New(database *sql.DB) *Service {
	return &Service{database: database}
}

func (service *Service) SetupRequired(ctx context.Context) (bool, error) {
	var ownerExists bool
	err := service.database.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE role = 'owner' AND status = 'active')").Scan(&ownerExists)
	if err != nil {
		return false, fmt.Errorf("check owner setup: %w", err)
	}
	return !ownerExists, nil
}

func (service *Service) Claim(ctx context.Context, input ClaimInput) (User, string, error) {
	email, displayName, err := validateClaim(input)
	if err != nil {
		return User{}, "", err
	}
	required, err := service.SetupRequired(ctx)
	if err != nil {
		return User{}, "", err
	}
	if !required {
		return User{}, "", ErrSetupComplete
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return User{}, "", ErrTooManyAttempts
	}
	passwordHash, err := credentials.HashPassword(input.Password)
	if err != nil {
		return User{}, "", err
	}
	userID, err := credentials.RandomToken(16)
	if err != nil {
		return User{}, "", err
	}
	sessionToken, err := credentials.RandomToken(32)
	if err != nil {
		return User{}, "", err
	}

	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("begin owner setup: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	var existingOwners int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'owner' AND status = 'active'").Scan(&existingOwners); err != nil {
		return User{}, "", fmt.Errorf("check owner setup: %w", err)
	}
	if existingOwners != 0 {
		return User{}, "", ErrSetupComplete
	}
	user := User{ID: "usr_" + userID, Email: email, DisplayName: displayName, Role: "owner", Status: "active", Grants: Grants{Unrestricted: true, Scopes: []string{}, ModelPatterns: []string{}, ConnectionIDs: []string{}}}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO users (id, email, display_name, password_hash, role, status, inference_unrestricted, created_at, updated_at) VALUES (?, ?, ?, ?, 'owner', 'active', 1, ?, ?)",
		user.ID, user.Email, user.DisplayName, passwordHash, now, now,
	); err != nil {
		if strings.Contains(err.Error(), "one_active_owner") {
			return User{}, "", ErrSetupComplete
		}
		return User{}, "", fmt.Errorf("create owner: %w", err)
	}
	if err := createSession(ctx, tx, user.ID, sessionToken, now, "", 0); err != nil {
		return User{}, "", err
	}
	if err := insertAudit(ctx, tx, user.ID, "owner.setup_claim", user.ID, map[string]any{}); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("commit owner setup: %w", err)
	}
	return user, sessionToken, nil
}

func createSession(ctx context.Context, tx *sql.Tx, userID, token string, now int64, userAgent string, expectedAuthRevision int64) error {
	sessionID, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	verifier := credentials.Verifier(token)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO user_sessions (id, verifier, user_id, expires_at, created_at, last_seen_at, authenticated_at, auth_revision, user_agent)
		SELECT ?, ?, id, ?, ?, ?, ?, auth_revision, ? FROM users
		WHERE id = ? AND status = 'active' AND (? = 0 OR auth_revision = ?)`,
		"ses_"+sessionID, verifier[:], time.Now().Add(sessionLifetime).UnixMilli(), now, now, now, truncateUTF8(userAgent, 200), userID, expectedAuthRevision, expectedAuthRevision,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil || created != 1 {
		return ErrInvalidSession
	}
	return nil
}

func truncateUTF8(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

func (service *Service) Session(ctx context.Context, token string) (User, error) {
	session, err := service.SessionInfo(ctx, token)
	return session.User, err
}

func (service *Service) SessionInfo(ctx context.Context, token string) (AuthenticatedSession, error) {
	verifier := credentials.Verifier(token)
	var session AuthenticatedSession
	var scopesJSON, modelsJSON, connectionsJSON string
	now := time.Now().UnixMilli()
	err := service.database.QueryRowContext(ctx, `
		SELECT user_sessions.id, users.id, users.email, users.display_name, users.role, users.status,
		       user_sessions.created_at, user_sessions.last_seen_at, user_sessions.expires_at,
		       user_sessions.authenticated_at, users.auth_revision, user_sessions.user_agent,
		       users.inference_unrestricted, users.scopes_json, users.model_patterns_json, users.connection_ids_json
		FROM user_sessions JOIN users ON users.id = user_sessions.user_id
		WHERE user_sessions.verifier = ? AND user_sessions.expires_at > ? AND user_sessions.last_seen_at > ? AND users.status = 'active'
		  AND user_sessions.auth_revision = users.auth_revision`, verifier[:], now, now-sessionIdleLifetime.Milliseconds(),
	).Scan(&session.ID, &session.User.ID, &session.User.Email, &session.User.DisplayName, &session.User.Role, &session.User.Status, &session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt, &session.AuthenticatedAt, &session.AuthRevision, &session.UserAgent, &session.User.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthenticatedSession{}, ErrInvalidSession
	}
	if err != nil {
		return AuthenticatedSession{}, fmt.Errorf("read session: %w", err)
	}
	if err := decodeUserGrants(&session.User, scopesJSON, modelsJSON, connectionsJSON); err != nil {
		return AuthenticatedSession{}, fmt.Errorf("read session grants: %w", err)
	}
	if session.LastSeenAt <= now-sessionTouchAfter.Milliseconds() {
		if _, err := service.database.ExecContext(ctx, "UPDATE user_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at = ?", now, session.ID, session.LastSeenAt); err != nil {
			return AuthenticatedSession{}, fmt.Errorf("touch session: %w", err)
		}
		session.LastSeenAt = now
	}
	return session, nil
}

func decodeUserGrants(user *User, scopesJSON, modelsJSON, connectionsJSON string) error {
	if err := json.Unmarshal([]byte(scopesJSON), &user.Grants.Scopes); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(modelsJSON), &user.Grants.ModelPatterns); err != nil {
		return err
	}
	return json.Unmarshal([]byte(connectionsJSON), &user.Grants.ConnectionIDs)
}

func validateClaim(input ClaimInput) (string, string, error) {
	email, displayName, err := ValidateIdentity(input.Email, input.DisplayName)
	if err != nil {
		return "", "", err
	}
	if err := validatePassword(input.Password); err != nil {
		return "", "", err
	}
	return email, displayName, nil
}

// ValidateIdentity normalizes and validates the account identity shared by setup and user management.
func ValidateIdentity(rawEmail, rawDisplayName string) (string, string, error) {
	email := strings.ToLower(strings.TrimSpace(rawEmail))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return "", "", &InputError{"Enter a valid email address"}
	}
	displayName := strings.TrimSpace(rawDisplayName)
	if displayName == "" || len([]rune(displayName)) > 100 {
		return "", "", &InputError{"Display name must be between 1 and 100 characters"}
	}
	return email, displayName, nil
}
