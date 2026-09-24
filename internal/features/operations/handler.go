package operations

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
	mux.HandleFunc("GET /api/v1/admin/settings", handler.settings)
	mux.HandleFunc("PATCH /api/v1/admin/settings", handler.updateSettings)
	mux.HandleFunc("GET /api/v1/admin/backups", handler.backups)
	mux.HandleFunc("POST /api/v1/admin/backups", handler.runBackup)
	mux.HandleFunc("POST /api/v1/admin/retention", handler.runRetention)
	mux.HandleFunc("GET /api/v1/admin/audit", handler.audit)
	mux.HandleFunc("GET /api/v1/admin/status", handler.status)
	mux.HandleFunc("GET /api/v1/admin/diagnostics", handler.diagnostics)
	mux.HandleFunc("GET /api/v1/admin/config/export", handler.exportConfig)
	mux.HandleFunc("POST /api/v1/admin/config/preview", handler.previewConfig)
	mux.HandleFunc("POST /api/v1/admin/config/import", handler.importConfig)
}

func (handler *Handler) exportConfig(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorizeOwner(response, request, false, true); !ok {
		return
	}
	bundle, err := handler.service.ExportConfig(request.Context())
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("Content-Disposition", `attachment; filename="pocket-ai-gateway-config.json"`)
	auth.WriteJSON(response, http.StatusOK, bundle)
}

func (handler *Handler) previewConfig(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorizeOwner(response, request, true, false); !ok {
		return
	}
	var bundle ConfigBundle
	if !auth.DecodeJSONMax(response, request, &bundle, 1<<20) {
		return
	}
	preview, err := PreviewConfig(bundle)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"preview": preview})
}

func (handler *Handler) importConfig(response http.ResponseWriter, request *http.Request) {
	current, ok := handler.authorizeOwner(response, request, true, true)
	if !ok {
		return
	}
	var bundle ConfigBundle
	if !auth.DecodeJSONMax(response, request, &bundle, 1<<20) {
		return
	}
	preview, err := handler.service.ImportConfig(request.Context(), current.User.ID, bundle)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"imported": preview})
}

func (handler *Handler) settings(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorizeOwner(response, request, false, false); !ok {
		return
	}
	value, err := handler.service.Settings(request.Context())
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatInt(value.Revision, 10)+`"`)
	auth.WriteJSON(response, http.StatusOK, map[string]any{"settings": value})
}

func (handler *Handler) updateSettings(response http.ResponseWriter, request *http.Request) {
	current, ok := handler.authorizeOwner(response, request, true, true)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(strings.Trim(request.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || revision < 1 {
		auth.WriteError(response, http.StatusPreconditionRequired, "revision_required", "A current settings revision is required")
		return
	}
	var input Settings
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	value, err := handler.service.UpdateSettings(request.Context(), current.User.ID, revision, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatInt(value.Revision, 10)+`"`)
	auth.WriteJSON(response, http.StatusOK, map[string]any{"settings": value})
}

func (handler *Handler) backups(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorizeOwner(response, request, false, false); !ok {
		return
	}
	items, err := handler.service.ListBackups(request.Context())
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}

func (handler *Handler) runBackup(response http.ResponseWriter, request *http.Request) {
	current, ok := handler.authorizeOwner(response, request, true, true)
	if !ok {
		return
	}
	job, err := handler.service.RunBackup(request.Context(), current.User.ID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"backup": job})
}

func (handler *Handler) runRetention(response http.ResponseWriter, request *http.Request) {
	current, ok := handler.authorizeOwner(response, request, true, true)
	if !ok {
		return
	}
	counts, err := handler.service.RunRetention(request.Context(), current.User.ID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"deleted": counts})
}

func (handler *Handler) audit(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorize(response, request, false); !ok {
		return
	}
	query := request.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	from, fromOK := parseAuditTime(query.Get("from"))
	to, toOK := parseAuditTime(query.Get("to"))
	if !fromOK || !toOK {
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", "Audit time filters must use RFC 3339")
		return
	}
	items, next, err := handler.service.Audit(request.Context(), AuditQuery{Limit: limit, Cursor: query.Get("cursor"), Actor: query.Get("actor_user_id"), Action: query.Get("action"), Resource: query.Get("resource_type"), From: from, To: to})
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) diagnostics(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorize(response, request, false); !ok {
		return
	}
	value, err := handler.service.Diagnostics(request.Context())
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"diagnostics": value})
}

func (handler *Handler) status(response http.ResponseWriter, request *http.Request) {
	if _, ok := handler.authorize(response, request, false); !ok {
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"status": handler.service.RuntimeStatus(request.Context())})
}

func (handler *Handler) authorize(response http.ResponseWriter, request *http.Request, mutation bool) (auth.AuthenticatedSession, bool) {
	var current auth.AuthenticatedSession
	var ok bool
	if mutation {
		current, _, ok = handler.auth.AuthorizeMutation(response, request)
	} else {
		current, _, ok = handler.auth.Authorize(response, request)
	}
	if !ok {
		return current, false
	}
	if current.User.Role != "owner" && current.User.Role != "admin" {
		auth.WriteError(response, http.StatusForbidden, "forbidden", "Administrator access is required")
		return current, false
	}
	return current, true
}

func (handler *Handler) authorizeOwner(response http.ResponseWriter, request *http.Request, mutation, recent bool) (auth.AuthenticatedSession, bool) {
	current, ok := handler.authorize(response, request, mutation)
	if !ok {
		return current, false
	}
	if current.User.Role != "owner" {
		auth.WriteError(response, http.StatusForbidden, "forbidden", "Owner access is required")
		return current, false
	}
	if recent && current.AuthenticatedAt < time.Now().Add(-15*time.Minute).UnixMilli() {
		auth.WriteError(response, http.StatusForbidden, "reauthentication_required", "Sign in again before changing recovery settings")
		return current, false
	}
	return current, true
}

func parseAuditTime(value string) (int64, bool) {
	if value == "" {
		return 0, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed.UnixMilli(), err == nil
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "forbidden", "Operation is not permitted")
	case errors.Is(err, ErrConflict):
		auth.WriteError(response, http.StatusPreconditionFailed, "revision_conflict", "Settings changed; reload and try again")
	case errors.Is(err, ErrInvalid):
		auth.WriteError(response, http.StatusBadRequest, "invalid_settings", "Settings are invalid")
	case strings.Contains(err.Error(), "cursor"):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", "Cursor is invalid")
	case strings.Contains(err.Error(), "already running"):
		auth.WriteError(response, http.StatusConflict, "backup_running", "A backup is already running")
	case strings.Contains(err.Error(), "backup key") || strings.Contains(err.Error(), "S3"):
		auth.WriteError(response, http.StatusUnprocessableEntity, "backup_configuration", err.Error())
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "operations_unavailable", "Operations are temporarily unavailable")
	}
}
