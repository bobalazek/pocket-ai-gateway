package auth

import (
	"context"
	"crypto/subtle"
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
	setupLifetime       = 15 * time.Minute
	sessionLifetime     = 24 * time.Hour
	sessionIdleLifetime = 30 * time.Minute
	sessionTouchAfter   = 5 * time.Minute
)

var (
	ErrSetupComplete    = errors.New("owner setup is already complete")
	ErrInvalidSetupCode = errors.New("setup code is invalid or expired")
	ErrInvalidSession   = errors.New("session is invalid or expired")
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

type SetupState struct {
	Required    bool
	Recoverable bool
}

type ClaimInput struct {
	SetupCode   string `json:"setup_code"`
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
	state, err := service.SetupStatus(ctx)
	return state.Required, err
}

func (service *Service) SetupStatus(ctx context.Context) (SetupState, error) {
	var ownerExists, recoverable bool
	err := service.database.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM users WHERE role = 'owner' AND status = 'active'),
		       EXISTS(SELECT 1 FROM setup_tokens WHERE claimed_user_id IS NOT NULL AND expires_at > ?)`,
		time.Now().UnixMilli(),
	).Scan(&ownerExists, &recoverable)
	if err != nil {
		return SetupState{}, fmt.Errorf("check owner setup: %w", err)
	}
	return SetupState{Required: !ownerExists, Recoverable: ownerExists && recoverable}, nil
}

func (service *Service) PrepareSetup(ctx context.Context) (code string, required bool, err error) {
	if _, err = service.database.ExecContext(ctx, "DELETE FROM setup_tokens WHERE expires_at <= ?", time.Now().UnixMilli()); err != nil {
		return "", false, fmt.Errorf("remove expired setup token: %w", err)
	}
	required, err = service.SetupRequired(ctx)
	if err != nil || !required {
		return "", required, err
	}
	code, err = credentials.RandomToken(24)
	if err != nil {
		return "", true, err
	}
	verifier := credentials.Verifier(code)
	_, err = service.database.ExecContext(ctx,
		"INSERT INTO setup_tokens (singleton, verifier, expires_at, claimed_user_id) VALUES (1, ?, ?, NULL) ON CONFLICT(singleton) DO UPDATE SET verifier = excluded.verifier, expires_at = excluded.expires_at, claimed_user_id = NULL",
		verifier[:], time.Now().Add(setupLifetime).UnixMilli(),
	)
	if err != nil {
		return "", true, fmt.Errorf("store setup token: %w", err)
	}
	return code, true, nil
}

func (service *Service) Claim(ctx context.Context, input ClaimInput) (User, string, error) {
	email, displayName, err := validateClaim(input)
	if err != nil {
		return User{}, "", err
	}
	state, err := service.SetupStatus(ctx)
	if err != nil {
		return User{}, "", err
	}
	if !state.Required && !state.Recoverable {
		return User{}, "", ErrSetupComplete
	}
	claimedUserID, err := validateSetupCode(ctx, service.database, input.SetupCode)
	if err != nil {
		return User{}, "", err
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return User{}, "", ErrTooManyAttempts
	}
	var passwordHash, userID string
	if claimedUserID == "" {
		passwordHash, err = credentials.HashPassword(input.Password)
		if err != nil {
			return User{}, "", err
		}
		userID, err = credentials.RandomToken(16)
		if err != nil {
			return User{}, "", err
		}
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

	claimedUserID, err = validateSetupCode(ctx, tx, input.SetupCode)
	if err != nil {
		return User{}, "", err
	}

	now := time.Now().UnixMilli()
	if claimedUserID != "" {
		user, storedPasswordHash, err := readClaimedUser(ctx, tx, claimedUserID)
		if err != nil {
			return User{}, "", err
		}
		passwordMatches, err := credentials.VerifyPassword(input.Password, storedPasswordHash)
		if err != nil {
			return User{}, "", err
		}
		if user.Email != email || user.DisplayName != displayName || !passwordMatches {
			return User{}, "", ErrInvalidSetupCode
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", user.ID); err != nil {
			return User{}, "", fmt.Errorf("replace owner setup session: %w", err)
		}
		if err := createSession(ctx, tx, user.ID, sessionToken, now, "", 0); err != nil {
			return User{}, "", err
		}
		if err := insertAudit(ctx, tx, user.ID, "owner.setup_replay", user.ID, map[string]any{"sessions_replaced": true}); err != nil {
			return User{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return User{}, "", fmt.Errorf("commit owner setup retry: %w", err)
		}
		return user, sessionToken, nil
	}

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
	if _, err := tx.ExecContext(ctx, "UPDATE setup_tokens SET claimed_user_id = ?, expires_at = ? WHERE singleton = 1", user.ID, time.Now().Add(setupLifetime).UnixMilli()); err != nil {
		return User{}, "", fmt.Errorf("mark setup token claimed: %w", err)
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

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateSetupCode(ctx context.Context, database rowQueryer, code string) (string, error) {
	var expected []byte
	var expiresAt int64
	var claimedUserID sql.NullString
	if err := database.QueryRowContext(ctx, "SELECT verifier, expires_at, claimed_user_id FROM setup_tokens WHERE singleton = 1").Scan(&expected, &expiresAt, &claimedUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrInvalidSetupCode
		}
		return "", fmt.Errorf("read setup token: %w", err)
	}
	actual := credentials.Verifier(strings.TrimSpace(code))
	if time.Now().UnixMilli() >= expiresAt || subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return "", ErrInvalidSetupCode
	}
	return claimedUserID.String, nil
}

func readClaimedUser(ctx context.Context, database rowQueryer, userID string) (User, string, error) {
	var user User
	var passwordHash string
	var scopesJSON, modelsJSON, connectionsJSON string
	err := database.QueryRowContext(ctx, "SELECT id, email, display_name, role, status, password_hash, inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json FROM users WHERE id = ? AND status = 'active'", userID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &passwordHash, &user.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrSetupComplete
	}
	if err != nil {
		return User{}, "", fmt.Errorf("read claimed owner: %w", err)
	}
	if err := decodeUserGrants(&user, scopesJSON, modelsJSON, connectionsJSON); err != nil {
		return User{}, "", err
	}
	return user, passwordHash, nil
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
