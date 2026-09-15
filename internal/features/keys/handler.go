package keys

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
	mux.HandleFunc("GET /api/v1/keys", handler.list)
	mux.HandleFunc("POST /api/v1/keys", handler.create)
	mux.HandleFunc("GET /api/v1/keys/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/keys/{id}", handler.update)
	mux.HandleFunc("DELETE /api/v1/keys/{id}", handler.revoke)
	mux.HandleFunc("POST /api/v1/keys/{id}/rotate", handler.rotate)
}

func (handler *Handler) list(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	items, next, err := handler.service.List(request.Context(), current.User.ID, limit, request.URL.Query().Get("cursor"))
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
	if !recentlyAuthenticated(response, current) {
		return
	}
	var input Input
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	key, secret, err := handler.service.Create(request.Context(), current.User.ID, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("ETag", auth.ETag(key.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"key": key, "secret": secret})
}

func (handler *Handler) get(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	key, err := handler.service.Get(request.Context(), current.User.ID, request.PathValue("id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(key.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"key": key})
}

func (handler *Handler) update(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	var input Input
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	key, err := handler.service.Update(request.Context(), current.User.ID, request.PathValue("id"), revision, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(key.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"key": key})
}

func (handler *Handler) revoke(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	if err := handler.service.Revoke(request.Context(), current.User.ID, request.PathValue("id"), revision); err != nil {
		handler.writeError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) rotate(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if !recentlyAuthenticated(response, current) {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	key, secret, err := handler.service.Rotate(request.Context(), current.User.ID, request.PathValue("id"), revision)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("ETag", auth.ETag(key.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"key": key, "secret": secret})
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		auth.WriteError(response, http.StatusNotFound, "not_found", "API key not found")
	case errors.Is(err, ErrExpired):
		auth.WriteError(response, http.StatusConflict, "key_expired", "Expired API keys cannot be rotated")
	case errors.Is(err, ErrConflict):
		auth.WriteError(response, http.StatusConflict, "revision_conflict", "The API key changed; reload and try again")
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "grant_denied", "API key grants must stay within your user grants")
	case strings.Contains(err.Error(), "cursor") || strings.Contains(err.Error(), "must be") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "expiry") || strings.Contains(err.Error(), "grant"):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "keys_unavailable", "API keys are unavailable")
	}
}

func recentlyAuthenticated(response http.ResponseWriter, current auth.AuthenticatedSession) bool {
	if current.AuthenticatedAt < time.Now().Add(-15*time.Minute).UnixMilli() {
		auth.WriteError(response, http.StatusForbidden, "reauthentication_required", "Sign in again before creating or rotating key material")
		return false
	}
	return true
}
