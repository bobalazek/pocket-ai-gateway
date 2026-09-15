package providers

import (
	"errors"
	"net/http"
	"strings"

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
	mux.HandleFunc("GET /api/v1/providers", handler.providerTypes)
	mux.HandleFunc("GET /api/v1/provider-presets", handler.providerPresets)
	mux.HandleFunc("GET /api/v1/connections", handler.listConnections)
	mux.HandleFunc("POST /api/v1/connections", handler.createConnection)
	mux.HandleFunc("GET /api/v1/connections/{id}", handler.getConnection)
	mux.HandleFunc("PATCH /api/v1/connections/{id}", handler.updateConnection)
	mux.HandleFunc("PUT /api/v1/connections/{id}/credential", handler.putCredential)
	mux.HandleFunc("GET /api/v1/connections/{id}/models", handler.listUpstreamModels)
	mux.HandleFunc("POST /api/v1/connections/{id}/models", handler.createUpstreamModel)
	mux.HandleFunc("GET /api/v1/models", handler.listPublicModels)
	mux.HandleFunc("POST /api/v1/models", handler.createPublicModel)
	mux.HandleFunc("GET /api/v1/admin/models", handler.listManagedPublicModels)
	mux.HandleFunc("GET /api/v1/admin/models/{id}/route", handler.getRoute)
	mux.HandleFunc("PUT /api/v1/admin/models/{id}/route", handler.putRoute)
	mux.HandleFunc("POST /api/v1/admin/models/{id}/route-preview", handler.previewRoute)
	mux.HandleFunc("GET /api/v1/admin/catalog", handler.getCatalog)
	mux.HandleFunc("PUT /api/v1/admin/catalog", handler.configureCatalog)
	mux.HandleFunc("POST /api/v1/admin/catalog/refresh", handler.refreshCatalog)
}

func (handler *Handler) providerTypes(response http.ResponseWriter, request *http.Request) {
	if _, _, ok := handler.auth.Authorize(response, request); !ok {
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": ProviderTypes()})
}
func (handler *Handler) providerPresets(response http.ResponseWriter, request *http.Request) {
	if _, _, ok := handler.auth.Authorize(response, request); !ok {
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": Presets()})
}
func (handler *Handler) listConnections(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.ListConnections(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}
func (handler *Handler) getConnection(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	item, err := handler.service.GetConnection(request.Context(), current.User, request.PathValue("id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(item.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"connection": item})
}
func (handler *Handler) createConnection(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input ConnectionInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	item, err := handler.service.CreateConnection(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(item.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"connection": item})
}
func (handler *Handler) updateConnection(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	var input ConnectionInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	item, err := handler.service.UpdateConnection(request.Context(), current.User, request.PathValue("id"), revision, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(item.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"connection": item})
}
func (handler *Handler) putCredential(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		Credential  string `json:"credential"`
		ExternalRef string `json:"external_ref"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	if err := handler.service.PutCredential(request.Context(), current.User, request.PathValue("id"), input.Credential, input.ExternalRef); err != nil {
		handler.writeError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
func (handler *Handler) listUpstreamModels(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.ListUpstreamModels(request.Context(), current.User, request.PathValue("id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}
func (handler *Handler) createUpstreamModel(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		UpstreamID   string   `json:"upstream_id"`
		Capabilities []string `json:"capabilities"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	item, err := handler.service.CreateUpstreamModel(request.Context(), current.User, request.PathValue("id"), input.UpstreamID, input.Capabilities)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"model": item})
}
func (handler *Handler) listPublicModels(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.ListVisibleModels(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}
func (handler *Handler) createPublicModel(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		ID            string   `json:"id"`
		Label         string   `json:"label"`
		Description   string   `json:"description"`
		TargetModelID string   `json:"target_model_id"`
		Capabilities  []string `json:"capabilities"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	item, err := handler.service.CreatePublicModel(request.Context(), current.User, input.ID, input.Label, input.Description, input.TargetModelID, input.Capabilities)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(item.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"model": item})
}

func (handler *Handler) listManagedPublicModels(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.ListManagedPublicModels(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}

func (handler *Handler) getRoute(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	model, targets, err := handler.service.RouteConfig(request.Context(), current.User, request.PathValue("id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(model.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"model": model, "targets": targets})
}

func (handler *Handler) putRoute(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	var input RouteConfigInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	model, err := handler.service.ConfigureRoute(request.Context(), current.User, request.PathValue("id"), revision, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(model.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"model": model})
}

func (handler *Handler) previewRoute(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if _, _, err := handler.service.RouteConfig(request.Context(), current.User, request.PathValue("id")); err != nil {
		handler.writeError(response, err)
		return
	}
	var input struct {
		Operation             string `json:"operation"`
		Streaming             bool   `json:"streaming"`
		EstimatedInputTokens  int64  `json:"estimated_input_tokens"`
		EstimatedOutputTokens int64  `json:"estimated_output_tokens"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	if input.Operation == "" || input.EstimatedInputTokens < 0 || input.EstimatedOutputTokens < 0 {
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", "operation and non-negative estimates are required")
		return
	}
	plan, err := handler.service.Route(request.Context(), request.PathValue("id"), RouteOptions{Operation: input.Operation, Streaming: input.Streaming, EstimatedInputTokens: input.EstimatedInputTokens, EstimatedOutputTokens: input.EstimatedOutputTokens, Seed: "preview"})
	if err != nil && !errors.Is(err, ErrNotFound) {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"route": plan})
}

func (handler *Handler) getCatalog(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, state, err := handler.service.Catalog(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "state": state})
}

func (handler *Handler) configureCatalog(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input struct {
		SourceURL            string `json:"source_url"`
		RefreshEnabled       bool   `json:"refresh_enabled"`
		RefreshIntervalHours int64  `json:"refresh_interval_hours"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	state, err := handler.service.ConfigureCatalog(request.Context(), current.User, input.SourceURL, input.RefreshEnabled, input.RefreshIntervalHours)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"state": state})
}

func (handler *Handler) refreshCatalog(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	state, err := handler.service.RefreshCatalog(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"state": state})
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		auth.WriteError(response, http.StatusNotFound, "not_found", "Provider resource not found")
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "forbidden", "You do not have permission to manage providers")
	case errors.Is(err, ErrConflict):
		auth.WriteError(response, http.StatusConflict, "revision_conflict", "The connection changed; reload and try again")
	case strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "must") || strings.Contains(err.Error(), "provide") || strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "overlap"):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "providers_unavailable", "Providers are unavailable")
	}
}
