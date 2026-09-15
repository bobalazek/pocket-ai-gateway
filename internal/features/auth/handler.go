package auth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const (
	SessionCookie = "pocket_ai_gateway_session"
	CSRFCookie    = "pocket_ai_gateway_csrf"
)

type Handler struct {
	service *Service
	origin  *url.URL
}

func NewHandler(service *Service, publicOrigin ...string) *Handler {
	handler := &Handler{service: service}
	if len(publicOrigin) > 0 && publicOrigin[0] != "" {
		handler.origin, _ = url.Parse(publicOrigin[0])
	}
	return handler
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/setup/status", handler.setupStatus)
	mux.HandleFunc("POST /api/v1/auth/setup/claim", handler.claim)
	mux.HandleFunc("POST /api/v1/auth/login", handler.login)
	mux.HandleFunc("POST /api/v1/auth/activate", handler.activate)
	mux.HandleFunc("POST /api/v1/auth/logout", handler.logout)
	mux.HandleFunc("GET /api/v1/auth/session", handler.session)
	mux.HandleFunc("GET /api/v1/auth/sessions", handler.sessions)
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{id}", handler.revokeSession)
	mux.HandleFunc("POST /api/v1/auth/password", handler.changePassword)
	mux.HandleFunc("GET /api/v1/me", handler.me)
	mux.HandleFunc("PATCH /api/v1/me", handler.updateProfile)
}

func (handler *Handler) setupStatus(response http.ResponseWriter, request *http.Request) {
	required, err := handler.service.SetupRequired(request.Context())
	if err != nil {
		WriteError(response, http.StatusServiceUnavailable, "storage_unavailable", "Setup status is unavailable")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusOK, map[string]bool{"setup_required": required})
}

func (handler *Handler) claim(response http.ResponseWriter, request *http.Request) {
	if !handler.sameOrigin(request) {
		WriteError(response, http.StatusForbidden, "origin_denied", "Request origin is not allowed")
		return
	}
	var input ClaimInput
	if !DecodeJSON(response, request, &input) {
		return
	}
	user, token, err := handler.service.Claim(request.Context(), input)
	if err != nil {
		writeAuthError(response, err, "setup_failed", "Owner setup could not be completed")
		return
	}
	if !handler.setSessionCookies(response, request, token) {
		WriteError(response, http.StatusServiceUnavailable, "session_failed", "Session could not be created")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusCreated, map[string]any{"user": user})
}

func (handler *Handler) login(response http.ResponseWriter, request *http.Request) {
	if !handler.sameOrigin(request) {
		WriteError(response, http.StatusForbidden, "origin_denied", "Request origin is not allowed")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !DecodeJSON(response, request, &input) {
		return
	}
	user, token, err := handler.service.Login(request.Context(), LoginInput{Email: input.Email, Password: input.Password, UserAgent: request.UserAgent(), Source: request.RemoteAddr})
	if err != nil {
		writeAuthError(response, err, "login_failed", "Login is unavailable")
		return
	}
	if !handler.setSessionCookies(response, request, token) {
		WriteError(response, http.StatusServiceUnavailable, "session_failed", "Session could not be created")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) activate(response http.ResponseWriter, request *http.Request) {
	if !handler.sameOrigin(request) {
		WriteError(response, http.StatusForbidden, "origin_denied", "Request origin is not allowed")
		return
	}
	var input struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !DecodeJSON(response, request, &input) {
		return
	}
	user, token, err := handler.service.Activate(request.Context(), ActivateInput{Code: input.Code, Password: input.Password, UserAgent: request.UserAgent()})
	if err != nil {
		writeAuthError(response, err, "activation_failed", "Activation could not be completed")
		return
	}
	if !handler.setSessionCookies(response, request, token) {
		WriteError(response, http.StatusServiceUnavailable, "session_failed", "Session could not be created")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) logout(response http.ResponseWriter, request *http.Request) {
	_, token, ok := handler.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if err := handler.service.Logout(request.Context(), token); err != nil {
		WriteError(response, http.StatusServiceUnavailable, "logout_failed", "Logout could not be completed")
		return
	}
	handler.ClearCookies(response, request)
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) session(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.Authorize(response, request)
	if !ok {
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusOK, map[string]any{"user": current.User, "session": CurrentSessionView(current)})
}

func (handler *Handler) sessions(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.Sessions(request.Context(), current)
	if err != nil {
		WriteError(response, http.StatusServiceUnavailable, "sessions_unavailable", "Sessions are unavailable")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	WriteJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) revokeSession(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	removed, err := handler.service.RevokeSession(request.Context(), current.User.ID, request.PathValue("id"))
	if err != nil {
		WriteError(response, http.StatusServiceUnavailable, "session_revoke_failed", "Session could not be revoked")
		return
	}
	if !removed {
		WriteError(response, http.StatusNotFound, "not_found", "Session not found")
		return
	}
	if request.PathValue("id") == current.ID {
		handler.ClearCookies(response, request)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) changePassword(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !DecodeJSON(response, request, &input) {
		return
	}
	token, err := handler.service.ChangePassword(request.Context(), current, input.CurrentPassword, input.NewPassword, request.UserAgent())
	if err != nil {
		handler.writeCredentialError(response, request, err, "password_change_failed", "Password could not be changed")
		return
	}
	if !handler.setSessionCookies(response, request, token) {
		WriteError(response, http.StatusServiceUnavailable, "session_failed", "Session could not be created")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) updateProfile(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		Email           string `json:"email"`
		DisplayName     string `json:"display_name"`
		CurrentPassword string `json:"current_password"`
	}
	if !DecodeJSON(response, request, &input) {
		return
	}
	user, token, err := handler.service.UpdateProfile(request.Context(), current, ProfileInput{Email: input.Email, DisplayName: input.DisplayName, CurrentPassword: input.CurrentPassword, UserAgent: request.UserAgent()})
	if err != nil {
		handler.writeCredentialError(response, request, err, "profile_update_failed", "Profile could not be updated")
		return
	}
	if token != "" && !handler.setSessionCookies(response, request, token) {
		WriteError(response, http.StatusServiceUnavailable, "session_failed", "Session could not be created")
		return
	}
	WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) me(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.Authorize(response, request)
	if ok {
		WriteJSON(response, http.StatusOK, map[string]any{"user": current.User})
	}
}

func (handler *Handler) Authorize(response http.ResponseWriter, request *http.Request) (AuthenticatedSession, string, bool) {
	cookie, err := request.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		WriteError(response, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return AuthenticatedSession{}, "", false
	}
	current, err := handler.service.SessionInfo(request.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, ErrInvalidSession) {
			handler.ClearCookies(response, request)
			WriteError(response, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		} else {
			WriteError(response, http.StatusServiceUnavailable, "storage_unavailable", "Session is unavailable")
		}
		return AuthenticatedSession{}, "", false
	}
	return current, cookie.Value, true
}

func (handler *Handler) AuthorizeMutation(response http.ResponseWriter, request *http.Request) (AuthenticatedSession, string, bool) {
	current, token, ok := handler.Authorize(response, request)
	if !ok {
		return AuthenticatedSession{}, "", false
	}
	if !handler.sameOrigin(request) || !ValidCSRF(request) {
		WriteError(response, http.StatusForbidden, "csrf_denied", "Request could not be verified")
		return AuthenticatedSession{}, "", false
	}
	return current, token, true
}

func (handler *Handler) setSessionCookies(response http.ResponseWriter, request *http.Request, sessionToken string) bool {
	csrf, err := credentials.RandomToken(24)
	if err != nil {
		return false
	}
	secure := request.TLS != nil || (handler.origin != nil && handler.origin.Scheme == "https")
	http.SetCookie(response, &http.Cookie{Name: SessionCookie, Value: sessionToken, Path: "/", MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	http.SetCookie(response, &http.Cookie{Name: CSRFCookie, Value: csrf, Path: "/", MaxAge: int(sessionLifetime.Seconds()), Secure: secure, SameSite: http.SameSiteStrictMode})
	return true
}

func (handler *Handler) ClearCookies(response http.ResponseWriter, request *http.Request) {
	secure := request.TLS != nil || (handler.origin != nil && handler.origin.Scheme == "https")
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(response, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == SessionCookie, Secure: secure, SameSite: http.SameSiteStrictMode})
	}
}

func ValidCSRF(request *http.Request) bool {
	cookie, err := request.Cookie(CSRFCookie)
	header := request.Header.Get("X-CSRF-Token")
	return err == nil && cookie.Value != "" && len(cookie.Value) == len(header) && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}

func (handler *Handler) sameOrigin(request *http.Request) bool {
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if handler.origin != nil {
		return parsed.Scheme == handler.origin.Scheme && parsed.Host == handler.origin.Host
	}
	if parsed.Host != request.Host {
		return false
	}
	expectedScheme := "http"
	if request.TLS != nil {
		expectedScheme = "https"
	}
	return parsed.Scheme == expectedScheme
}

func DecodeJSON(response http.ResponseWriter, request *http.Request, destination any) bool {
	return DecodeJSONMax(response, request, destination, 64<<10)
}

func DecodeJSONMax(response http.ResponseWriter, request *http.Request, destination any, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(response, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large")
			return false
		}
		WriteError(response, http.StatusBadRequest, "invalid_request", "Request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(response, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large")
			return false
		}
		WriteError(response, http.StatusBadRequest, "invalid_request", "Request body contains trailing data")
		return false
	}
	return true
}

func writeAuthError(response http.ResponseWriter, err error, fallbackCode, fallbackMessage string) {
	var inputError *InputError
	switch {
	case errors.Is(err, ErrSetupComplete):
		WriteError(response, http.StatusConflict, "setup_complete", "Owner setup is already complete")
	case errors.Is(err, ErrInvalidActivation):
		WriteError(response, http.StatusUnauthorized, "invalid_activation_code", "Activation code is invalid or expired")
	case errors.Is(err, ErrInvalidCredentials):
		WriteError(response, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
	case errors.Is(err, ErrTooManyAttempts):
		response.Header().Set("Retry-After", "900")
		WriteError(response, http.StatusTooManyRequests, "too_many_attempts", "Try again later")
	case errors.As(err, &inputError):
		WriteError(response, http.StatusBadRequest, "invalid_request", inputError.Error())
	default:
		WriteError(response, http.StatusServiceUnavailable, fallbackCode, fallbackMessage)
	}
}

func (handler *Handler) writeCredentialError(response http.ResponseWriter, request *http.Request, err error, fallbackCode, fallbackMessage string) {
	if errors.Is(err, ErrInvalidSession) {
		handler.ClearCookies(response, request)
		WriteError(response, http.StatusUnauthorized, "session_expired", "Sign in again before changing account credentials")
		return
	}
	writeAuthError(response, err, fallbackCode, fallbackMessage)
}

func WriteError(response http.ResponseWriter, status int, code, message string) {
	WriteJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func WriteJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

// RequireRevision reads the optimistic-concurrency revision from If-Match.
func RequireRevision(response http.ResponseWriter, request *http.Request) (int64, bool) {
	revision, err := strconv.ParseInt(strings.Trim(request.Header.Get("If-Match"), "\""), 10, 64)
	if err != nil || revision < 1 {
		WriteError(response, http.StatusPreconditionRequired, "revision_required", "Send the current ETag in If-Match")
		return 0, false
	}
	return revision, true
}

// ETag formats a revision for optimistic concurrency responses.
func ETag(revision int64) string { return "\"" + strconv.FormatInt(revision, 10) + "\"" }
