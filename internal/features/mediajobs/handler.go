package mediajobs

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

type Handler struct {
	service *Service
	keys    *keys.Service
	auth    *auth.Handler
}

func NewHandler(service *Service, keyService *keys.Service, authHandler *auth.Handler) *Handler {
	return &Handler{service: service, keys: keyService, auth: authHandler}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/media/jobs", handler.create)
	mux.HandleFunc("GET /api/v1/media/jobs", handler.list)
	mux.HandleFunc("GET /api/v1/media/jobs/{id}", handler.get)
	mux.HandleFunc("POST /api/v1/media/jobs/{id}/cancel", handler.cancel)
}

func (handler *Handler) create(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.inferencePrincipal(response, request)
	if !ok {
		return
	}
	var input CreateInput
	if !auth.DecodeJSONMax(response, request, &input, maxInputBytes+(16<<10)) {
		return
	}
	job, err := handler.service.Create(request.Context(), principal, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Location", "/api/v1/media/jobs/"+job.ID)
	response.Header().Set("X-Pocket-AI-Request-ID", job.RequestID)
	auth.WriteJSON(response, http.StatusAccepted, map[string]any{"job": job})
}

func (handler *Handler) list(response http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if handler.hasBearer(request) {
		principal, ok := handler.inferencePrincipal(response, request)
		if !ok {
			return
		}
		items, err := handler.service.List(request.Context(), principal, limit)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
		return
	}
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok || !handler.requireManager(response, current.User) {
		return
	}
	items, err := handler.service.AdminList(request.Context(), limit)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}

func (handler *Handler) get(response http.ResponseWriter, request *http.Request) {
	var job Job
	var err error
	if handler.hasBearer(request) {
		principal, ok := handler.inferencePrincipal(response, request)
		if !ok {
			return
		}
		job, err = handler.service.Get(request.Context(), principal, request.PathValue("id"))
	} else {
		current, _, ok := handler.auth.Authorize(response, request)
		if !ok || !handler.requireManager(response, current.User) {
			return
		}
		job, err = handler.service.AdminGet(request.Context(), request.PathValue("id"))
	}
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"job": job})
}

func (handler *Handler) cancel(response http.ResponseWriter, request *http.Request) {
	var job Job
	var err error
	if handler.hasBearer(request) {
		principal, ok := handler.inferencePrincipal(response, request)
		if !ok {
			return
		}
		job, err = handler.service.Cancel(request.Context(), principal, request.PathValue("id"))
	} else {
		current, _, ok := handler.auth.AuthorizeMutation(response, request)
		if !ok || !handler.requireManager(response, current.User) {
			return
		}
		job, err = handler.service.AdminCancel(request.Context(), request.PathValue("id"))
	}
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"job": job})
}

func (handler *Handler) inferencePrincipal(response http.ResponseWriter, request *http.Request) (keys.Principal, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		auth.WriteError(response, http.StatusUnauthorized, "authentication_error", "Provide one gateway API key")
		return keys.Principal{}, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer "))
	principal, err := handler.keys.Authenticate(request.Context(), token)
	if err != nil {
		auth.WriteError(response, http.StatusUnauthorized, "authentication_error", "API key is invalid")
		return keys.Principal{}, false
	}
	if !principal.AllowsScope(Scope) {
		auth.WriteError(response, http.StatusForbidden, "permission_denied", "Media job access is not permitted")
		return keys.Principal{}, false
	}
	return principal, true
}

func (*Handler) hasBearer(request *http.Request) bool {
	return len(request.Header.Values("Authorization")) > 0
}

func (*Handler) requireManager(response http.ResponseWriter, user auth.User) bool {
	if user.Role != "owner" && user.Role != "admin" {
		auth.WriteError(response, http.StatusForbidden, "permission_denied", "Administrator access is required")
		return false
	}
	return true
}

func (*Handler) writeError(response http.ResponseWriter, err error) {
	var denial *usage.Denial
	switch {
	case errors.Is(err, ErrNotFound):
		auth.WriteError(response, http.StatusNotFound, "not_found", "Media job not found")
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "permission_denied", "Media job access is not permitted")
	case errors.Is(err, ErrInvalid):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", "model, media_type, and an input object are required")
	case errors.Is(err, usage.ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "permission_denied", "Media job access is not permitted")
	case errors.As(err, &denial):
		if denial.RetryAfterSecond != nil {
			response.Header().Set("Retry-After", strconv.FormatInt(*denial.RetryAfterSecond, 10))
		}
		auth.WriteError(response, http.StatusTooManyRequests, "rate_limit_exceeded", denial.Reason)
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "media_jobs_unavailable", "Media jobs are unavailable")
	}
}
