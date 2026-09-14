package users

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

type Handler struct {
	service *Service
	auth    *auth.Handler
}

func NewHandler(service *Service, authHandler *auth.Handler) *Handler {
	return &Handler{service: service, auth: authHandler}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/users", handler.list)
	mux.HandleFunc("POST /api/v1/admin/users", handler.create)
	mux.HandleFunc("GET /api/v1/admin/users/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", handler.update)
	mux.HandleFunc("PUT /api/v1/admin/users/{id}/grants", handler.updateGrants)
	mux.HandleFunc("POST /api/v1/admin/users/{id}/{action}", handler.issueCode)
	mux.HandleFunc("POST /api/v1/admin/owner/transfer", handler.transferOwner)
}

func (handler *Handler) updateGrants(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := requireRevision(response, request)
	if !ok {
		return
	}
	var grants Grants
	if !auth.DecodeJSON(response, request, &grants) {
		return
	}
	user, err := handler.service.UpdateGrants(request.Context(), current.User, request.PathValue("id"), revision, grants)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", etag(user.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) list(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	items, next, err := handler.service.List(request.Context(), current.User, limit, request.URL.Query().Get("cursor"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) create(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input CreateInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	user, code, err := handler.service.Create(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("ETag", etag(user.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"user": user, "activation_code": code})
}

func (handler *Handler) get(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	user, err := handler.service.Get(request.Context(), current.User, request.PathValue("id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", etag(user.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) update(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := requireRevision(response, request)
	if !ok {
		return
	}
	var input UpdateInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	user, err := handler.service.Update(request.Context(), current.User, request.PathValue("id"), revision, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", etag(user.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (handler *Handler) issueCode(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	purpose := strings.TrimSuffix(request.PathValue("action"), "-code")
	if purpose != "activation" && purpose != "recovery" {
		auth.WriteError(response, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	if purpose == "recovery" && current.AuthenticatedAt < time.Now().Add(-15*time.Minute).UnixMilli() {
		auth.WriteError(response, http.StatusForbidden, "reauthentication_required", "Sign in again before issuing a recovery code")
		return
	}
	code, err := handler.service.IssueCode(request.Context(), current.User, request.PathValue("id"), purpose)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	auth.WriteJSON(response, http.StatusCreated, map[string]string{"code": code})
}

func (handler *Handler) transferOwner(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if current.AuthenticatedAt < time.Now().Add(-15*time.Minute).UnixMilli() {
		auth.WriteError(response, http.StatusForbidden, "reauthentication_required", "Sign in again before transferring ownership")
		return
	}
	var input struct {
		UserID   string `json:"user_id"`
		Revision int64  `json:"revision"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	if err := handler.service.TransferOwner(request.Context(), current.User, input.UserID, input.Revision); err != nil {
		handler.writeError(response, err)
		return
	}
	handler.auth.ClearCookies(response, request)
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	var inputError *auth.InputError
	switch {
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "forbidden", "You do not have permission to perform this action")
	case errors.Is(err, ErrNotFound):
		auth.WriteError(response, http.StatusNotFound, "not_found", "User not found")
	case errors.Is(err, ErrConflict):
		auth.WriteError(response, http.StatusConflict, "revision_conflict", "The user changed; reload and try again")
	case errors.As(err, &inputError):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", inputError.Error())
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "users_unavailable", "Users are unavailable")
	}
}

func requireRevision(response http.ResponseWriter, request *http.Request) (int64, bool) {
	value := strings.Trim(request.Header.Get("If-Match"), "\"")
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 {
		auth.WriteError(response, http.StatusPreconditionRequired, "revision_required", "Send the current ETag in If-Match")
		return 0, false
	}
	return revision, true
}

func etag(revision int64) string { return "\"" + strconv.FormatInt(revision, 10) + "\"" }
