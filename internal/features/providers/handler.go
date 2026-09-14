package providers

import (
	"errors"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /api/v1/connections", handler.listConnections)
	mux.HandleFunc("POST /api/v1/connections", handler.createConnection)
	mux.HandleFunc("GET /api/v1/connections/{id}", handler.getConnection)
	mux.HandleFunc("PATCH /api/v1/connections/{id}", handler.updateConnection)
	mux.HandleFunc("PUT /api/v1/connections/{id}/credential", handler.putCredential)
	mux.HandleFunc("GET /api/v1/connections/{id}/models", handler.listUpstreamModels)
	mux.HandleFunc("POST /api/v1/connections/{id}/models", handler.createUpstreamModel)
	mux.HandleFunc("GET /api/v1/models", handler.listPublicModels)
	mux.HandleFunc("POST /api/v1/models", handler.createPublicModel)
}

func (handler *Handler) providerTypes(response http.ResponseWriter, request *http.Request) {
	if _, _, ok := handler.auth.Authorize(response, request); !ok {
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": ProviderTypes()})
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
	response.Header().Set("ETag", etag(item.Revision))
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
	response.Header().Set("ETag", etag(item.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"connection": item})
}
func (handler *Handler) updateConnection(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := revision(response, request)
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
	response.Header().Set("ETag", etag(item.Revision))
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
	response.Header().Set("ETag", etag(item.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"model": item})
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
func revision(response http.ResponseWriter, request *http.Request) (int64, bool) {
	value, err := strconv.ParseInt(strings.Trim(request.Header.Get("If-Match"), "\""), 10, 64)
	if err != nil || value < 1 {
		auth.WriteError(response, http.StatusPreconditionRequired, "revision_required", "Send the current ETag in If-Match")
		return 0, false
	}
	return value, true
}
func etag(value int64) string { return "\"" + strconv.FormatInt(value, 10) + "\"" }
