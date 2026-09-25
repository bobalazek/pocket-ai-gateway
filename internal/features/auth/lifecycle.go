package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	loginWindow      = 15 * time.Minute
	loginBlock       = 15 * time.Minute
	maxLoginFailures = 5
)

var (
	ErrInvalidCredentials = errors.New("email or password is incorrect")
	ErrInvalidActivation  = errors.New("activation code is invalid or expired")
	ErrTooManyAttempts    = errors.New("too many authentication attempts")
	ErrNotFound           = errors.New("resource not found")
	passwordSlots         = make(chan struct{}, 2)
	dummyHashOnce         sync.Once
	dummyHash             string
	dummyHashErr          error
)

type LoginInput struct {
	Email     string
	Password  string
	UserAgent string
	Source    string
}

type ActivateInput struct {
	Code      string
	Password  string
	UserAgent string
}

type SessionView struct {
	ID              string `json:"id"`
	Current         bool   `json:"current"`
	CreatedAt       string `json:"created_at"`
	LastSeenAt      string `json:"last_seen_at"`
	ExpiresAt       string `json:"expires_at"`
	AuthenticatedAt string `json:"authenticated_at"`
	UserAgent       string `json:"user_agent"`
}

type ProfileInput struct {
	Email           string
	DisplayName     string
	CurrentPassword string
	UserAgent       string
}

func (service *Service) Login(ctx context.Context, input LoginInput) (User, string, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	source := loginSource(input.Source)
	// No instance-wide subject: it would let anonymous requests lock every user out.
	for _, subject := range []string{"source:" + source, "email:" + email} {
		if err := service.checkLoginThrottle(ctx, subject); err != nil {
			return User{}, "", err
		}
	}
	if !validLoginEmail(email) || !validPasswordSize(input.Password) {
		return User{}, "", ErrInvalidCredentials
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return User{}, "", ErrTooManyAttempts
	}

	var user User
	var passwordHash string
	var authRevision int64
	var scopesJSON, modelsJSON, connectionsJSON string
	err := service.database.QueryRowContext(ctx, `
		SELECT id, email, display_name, role, status, password_hash, auth_revision,
		       inference_unrestricted, scopes_json, model_patterns_json, connection_ids_json
		FROM users WHERE email = ? AND status = 'active'`, email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &passwordHash, &authRevision, &user.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		passwordHash, err = invalidLoginHash()
	}
	if err != nil {
		return User{}, "", fmt.Errorf("read login identity: %w", err)
	}
	if user.ID != "" {
		if err := decodeUserGrants(&user, scopesJSON, modelsJSON, connectionsJSON); err != nil {
			return User{}, "", err
		}
	}
	matches, err := credentials.VerifyPassword(input.Password, passwordHash)
	if err != nil {
		return User{}, "", err
	}
	if user.ID == "" || !matches {
		if err := service.recordLoginFailures(ctx, email, source); err != nil {
			return User{}, "", err
		}
		return User{}, "", ErrInvalidCredentials
	}
	_, _ = service.database.ExecContext(ctx, "DELETE FROM login_throttles WHERE subject = ?", verifierBytes("email:"+email))
	return service.startSession(ctx, user, input.UserAgent, authRevision)
}

func (service *Service) recordLoginFailures(ctx context.Context, email, source string) error {
	for _, throttle := range []struct {
		subject string
		limit   int
	}{{"source:" + source, 20}, {"email:" + email, maxLoginFailures}} {
		if err := service.recordLoginFailure(ctx, throttle.subject, throttle.limit); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) Activate(ctx context.Context, input ActivateInput) (User, string, error) {
	if err := validatePassword(input.Password); err != nil {
		return User{}, "", err
	}
	verifier := credentials.Verifier(strings.TrimSpace(input.Code))
	var eligible bool
	if err := service.database.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM activation_tokens JOIN users ON users.id = activation_tokens.user_id
			WHERE activation_tokens.verifier = ? AND activation_tokens.expires_at > ?
			  AND ((activation_tokens.purpose = 'recovery' AND users.status = 'active')
			       OR (activation_tokens.purpose = 'activation' AND users.status = 'suspended'))
		)`, verifier[:], time.Now().UnixMilli()).Scan(&eligible); err != nil {
		return User{}, "", fmt.Errorf("check activation: %w", err)
	}
	if !eligible {
		return User{}, "", ErrInvalidActivation
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
	sessionToken, err := credentials.RandomToken(32)
	if err != nil {
		return User{}, "", err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("begin activation: %w", err)
	}
	defer tx.Rollback()

	var user User
	var purpose string
	var scopesJSON, modelsJSON, connectionsJSON string
	err = tx.QueryRowContext(ctx, `
		SELECT users.id, users.email, users.display_name, users.role, users.status, activation_tokens.purpose,
		       users.inference_unrestricted, users.scopes_json, users.model_patterns_json, users.connection_ids_json
		FROM activation_tokens JOIN users ON users.id = activation_tokens.user_id
		WHERE activation_tokens.verifier = ? AND activation_tokens.expires_at > ?`, verifier[:], time.Now().UnixMilli(),
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &purpose, &user.Grants.Unrestricted, &scopesJSON, &modelsJSON, &connectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrInvalidActivation
	}
	if err != nil {
		return User{}, "", fmt.Errorf("read activation: %w", err)
	}
	if err := decodeUserGrants(&user, scopesJSON, modelsJSON, connectionsJSON); err != nil {
		return User{}, "", err
	}
	if purpose == "activation" && user.Status != "suspended" {
		return User{}, "", ErrInvalidActivation
	}
	if purpose == "recovery" && user.Status != "active" {
		return User{}, "", ErrInvalidActivation
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, status = 'active', auth_revision = auth_revision + 1,
		       revision = revision + 1, updated_at = ? WHERE id = ?`, passwordHash, now, user.ID); err != nil {
		return User{}, "", fmt.Errorf("activate user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", user.ID); err != nil {
		return User{}, "", fmt.Errorf("revoke sessions during activation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM activation_tokens WHERE user_id = ?", user.ID); err != nil {
		return User{}, "", fmt.Errorf("consume activation: %w", err)
	}
	if err := insertAudit(ctx, tx, user.ID, "account."+purpose, user.ID, map[string]any{"sessions_revoked": true}); err != nil {
		return User{}, "", err
	}
	if err := createSession(ctx, tx, user.ID, sessionToken, now, input.UserAgent, 0); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("commit activation: %w", err)
	}
	user.Status = "active"
	return user, sessionToken, nil
}

func (service *Service) Logout(ctx context.Context, token string) error {
	verifier := credentials.Verifier(token)
	if _, err := service.database.ExecContext(ctx, "DELETE FROM user_sessions WHERE verifier = ?", verifier[:]); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (service *Service) PrepareOwnerRecovery(ctx context.Context, code string) error {
	verifier := credentials.Verifier(code)
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE role = 'owner' AND status = 'active'").Scan(&ownerID); err != nil {
		return fmt.Errorf("find active owner: %w", err)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "DELETE FROM activation_tokens WHERE user_id = ?", ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO activation_tokens (verifier, user_id, purpose, expires_at, created_at) VALUES (?, ?, 'recovery', ?, ?)", verifier[:], ownerID, now+24*time.Hour.Milliseconds(), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET auth_revision = auth_revision + 1, revision = revision + 1, updated_at = ? WHERE id = ?", now, ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", ownerID); err != nil {
		return err
	}
	auditID, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO audit_events (id, action, resource_type, resource_id, created_at) VALUES (?, 'owner.offline_recovery', 'user', ?, ?)", "aud_"+auditID, ownerID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) Sessions(ctx context.Context, current AuthenticatedSession) ([]SessionView, error) {
	rows, err := service.database.QueryContext(ctx, `
		SELECT id, created_at, last_seen_at, expires_at, authenticated_at, user_agent
		FROM user_sessions WHERE user_id = ? AND expires_at > ? AND last_seen_at > ? ORDER BY created_at DESC, id DESC`,
		current.User.ID, time.Now().UnixMilli(), time.Now().Add(-sessionIdleLifetime).UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	result := []SessionView{}
	for rows.Next() {
		var item SessionView
		var createdAt, lastSeenAt, expiresAt, authenticatedAt int64
		if err := rows.Scan(&item.ID, &createdAt, &lastSeenAt, &expiresAt, &authenticatedAt, &item.UserAgent); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		item.Current = item.ID == current.ID
		item.CreatedAt = formatTime(createdAt)
		item.LastSeenAt = formatTime(lastSeenAt)
		item.ExpiresAt = formatTime(expiresAt)
		item.AuthenticatedAt = formatTime(authenticatedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

func CurrentSessionView(current AuthenticatedSession) SessionView {
	return SessionView{
		ID: current.ID, Current: true, CreatedAt: formatTime(current.CreatedAt), LastSeenAt: formatTime(current.LastSeenAt),
		ExpiresAt: formatTime(current.ExpiresAt), AuthenticatedAt: formatTime(current.AuthenticatedAt), UserAgent: current.UserAgent,
	}
}

func (service *Service) RevokeSession(ctx context.Context, userID, sessionID string) (bool, error) {
	result, err := service.database.ExecContext(ctx, "DELETE FROM user_sessions WHERE id = ? AND user_id = ?", sessionID, userID)
	if err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return removed == 1, nil
}

func (service *Service) ChangePassword(ctx context.Context, current AuthenticatedSession, oldPassword, newPassword, userAgent string) (string, error) {
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return "", ErrTooManyAttempts
	}
	var existingHash string
	if err := service.database.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", current.User.ID).Scan(&existingHash); err != nil {
		return "", fmt.Errorf("read current password: %w", err)
	}
	matches, err := credentials.VerifyPassword(oldPassword, existingHash)
	if err != nil {
		return "", err
	}
	if !matches {
		return "", ErrInvalidCredentials
	}
	newHash, err := credentials.HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	token, err := credentials.RandomToken(32)
	if err != nil {
		return "", err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin password change: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, auth_revision = auth_revision + 1, revision = revision + 1, updated_at = ? WHERE id = ? AND auth_revision = ?`, newHash, now, current.User.ID, current.AuthRevision)
	if err != nil {
		return "", fmt.Errorf("change password: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", ErrInvalidSession
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", current.User.ID); err != nil {
		return "", fmt.Errorf("revoke old sessions: %w", err)
	}
	if err := insertAudit(ctx, tx, current.User.ID, "account.password_change", current.User.ID, map[string]any{"sessions_revoked": true}); err != nil {
		return "", err
	}
	if err := createSession(ctx, tx, current.User.ID, token, now, userAgent, 0); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit password change: %w", err)
	}
	return token, nil
}

func (service *Service) UpdateProfile(ctx context.Context, current AuthenticatedSession, input ProfileInput) (User, string, error) {
	email, displayName, err := ValidateIdentity(input.Email, input.DisplayName)
	if err != nil {
		return User{}, "", err
	}
	emailChanged := email != current.User.Email
	if emailChanged {
		select {
		case passwordSlots <- struct{}{}:
			defer func() { <-passwordSlots }()
		default:
			return User{}, "", ErrTooManyAttempts
		}
		var existingHash string
		if err := service.database.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", current.User.ID).Scan(&existingHash); err != nil {
			return User{}, "", fmt.Errorf("read current password: %w", err)
		}
		matches, err := credentials.VerifyPassword(input.CurrentPassword, existingHash)
		if err != nil {
			return User{}, "", err
		}
		if !matches {
			return User{}, "", ErrInvalidCredentials
		}
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	authBump := 0
	if emailChanged {
		authBump = 1
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET email = ?, display_name = ?, revision = revision + 1, auth_revision = auth_revision + ?, updated_at = ? WHERE id = ? AND auth_revision = ?", email, displayName, authBump, now, current.User.ID, current.AuthRevision)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", &InputError{Message: "A user with this email already exists"}
		}
		return User{}, "", fmt.Errorf("update profile: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return User{}, "", ErrInvalidSession
	}
	newToken := ""
	if emailChanged {
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id = ?", current.User.ID); err != nil {
			return User{}, "", err
		}
		newToken, err = credentials.RandomToken(32)
		if err != nil {
			return User{}, "", err
		}
		if err := createSession(ctx, tx, current.User.ID, newToken, now, input.UserAgent, 0); err != nil {
			return User{}, "", err
		}
	}
	if err := insertAudit(ctx, tx, current.User.ID, "account.update", current.User.ID, map[string]any{"email_changed": emailChanged}); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", err
	}
	current.User.Email, current.User.DisplayName = email, displayName
	return current.User, newToken, nil
}

func (service *Service) startSession(ctx context.Context, user User, userAgent string, expectedAuthRevision int64) (User, string, error) {
	token, err := credentials.RandomToken(32)
	if err != nil {
		return User{}, "", err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("begin session: %w", err)
	}
	defer tx.Rollback()
	if err := createSession(ctx, tx, user.ID, token, time.Now().UnixMilli(), userAgent, expectedAuthRevision); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("commit session: %w", err)
	}
	return user, token, nil
}

func (service *Service) checkLoginThrottle(ctx context.Context, email string) error {
	var blockedUntil int64
	err := service.database.QueryRowContext(ctx, "SELECT blocked_until FROM login_throttles WHERE subject = ?", verifierBytes(email)).Scan(&blockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read login throttle: %w", err)
	}
	if blockedUntil > time.Now().UnixMilli() {
		return ErrTooManyAttempts
	}
	return nil
}

func (service *Service) recordLoginFailure(ctx context.Context, subject string, limit int) error {
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin login throttle: %w", err)
	}
	defer tx.Rollback()
	var windowStarted int64
	var failures int
	if _, err := tx.ExecContext(ctx, "DELETE FROM login_throttles WHERE window_started_at < ? AND blocked_until <= ?", now-loginWindow.Milliseconds(), now); err != nil {
		return fmt.Errorf("clean login throttles: %w", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT window_started_at, failures FROM login_throttles WHERE subject = ?", verifierBytes(subject)).Scan(&windowStarted, &failures)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read login failures: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) || windowStarted+loginWindow.Milliseconds() <= now {
		windowStarted, failures = now, 0
	}
	failures++
	blockedUntil := int64(0)
	if failures >= limit {
		blockedUntil = now + loginBlock.Milliseconds()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO login_throttles (subject, window_started_at, failures, blocked_until) VALUES (?, ?, ?, ?)
		ON CONFLICT(subject) DO UPDATE SET window_started_at = excluded.window_started_at, failures = excluded.failures, blocked_until = excluded.blocked_until`,
		verifierBytes(subject), windowStarted, failures, blockedUntil); err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	return tx.Commit()
}

func invalidLoginHash() (string, error) {
	dummyHashOnce.Do(func() { dummyHash, dummyHashErr = credentials.HashPassword("invalid-login-password") })
	return dummyHash, dummyHashErr
}

func verifierBytes(value string) []byte {
	verifier := credentials.Verifier(value)
	return verifier[:]
}

func validLoginEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && len(email) <= 254
}

func loginSource(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}
	if remoteAddr == "" {
		return "unknown"
	}
	return truncateUTF8(remoteAddr, 200)
}

func validPasswordSize(password string) bool {
	return utf8.ValidString(password) && len(password) <= 1024
}

func validatePassword(password string) error {
	if !validPasswordSize(password) || utf8.RuneCountInString(password) < 12 {
		return &InputError{"Password must be at least 12 characters and no more than 1024 bytes"}
	}
	return nil
}

func formatTime(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format(time.RFC3339)
}

func insertAudit(ctx context.Context, tx *sql.Tx, actorID, action, resourceID string, detail any) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (id, actor_user_id, action, resource_type, resource_id, detail_json, created_at) VALUES (?, ?, ?, 'user', ?, ?, ?)", "aud_"+id, actorID, action, resourceID, string(encoded), time.Now().UnixMilli())
	return err
}
