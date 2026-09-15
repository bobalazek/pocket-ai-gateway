package usage

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
	mux.HandleFunc("GET /api/v1/admin/policies", handler.listPolicies)
	mux.HandleFunc("POST /api/v1/admin/policies", handler.createPolicy)
	mux.HandleFunc("PATCH /api/v1/admin/policies/{id}", handler.updatePolicy)
	mux.HandleFunc("GET /api/v1/keys/{id}/effective-limits", handler.effectiveLimits)
	mux.HandleFunc("GET /api/v1/usage", handler.summary)
	mux.HandleFunc("GET /api/v1/usage/unresolved", handler.unresolved)
	mux.HandleFunc("GET /api/v1/requests", handler.requests)
	mux.HandleFunc("GET /api/v1/admin/usage/outbox", handler.outbox)
	mux.HandleFunc("GET /api/v1/admin/prices", handler.listPrices)
	mux.HandleFunc("POST /api/v1/admin/prices", handler.createPrice)
	mux.HandleFunc("POST /api/v1/admin/usage/reprice-preview", handler.previewReprice)
	mux.HandleFunc("POST /api/v1/admin/usage/reprice", handler.applyReprice)
	mux.HandleFunc("POST /api/v1/admin/usage/adjustments", handler.adjust)
	mux.HandleFunc("POST /api/v1/admin/usage/reconciliations", handler.reconcile)
}

func (handler *Handler) requests(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	items, next, err := handler.service.ListRequests(request.Context(), current.User, UsageQuery{
		UserID: query.Get("user_id"), KeyID: query.Get("key_id"), ModelID: query.Get("model_id"),
		Dialect: query.Get("dialect"), Cursor: query.Get("cursor"), RequestID: query.Get("request_id"),
	})
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) reconcile(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if !requireRecentAuthentication(response, current, "reconciling usage") {
		return
	}
	var input struct {
		AttemptID      string `json:"attempt_id"`
		InputTokens    *int64 `json:"input_tokens"`
		OutputTokens   *int64 `json:"output_tokens"`
		CostUSD        string `json:"cost_usd"`
		UsageStatus    string `json:"usage_status"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	var cost *int64
	if input.CostUSD != "" {
		value, err := ParseUSD(input.CostUSD)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		cost = &value
	}
	if err := handler.service.ReconcileUnknown(request.Context(), current.User, input.AttemptID, ReconciliationInput{InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, CostNanos: cost, UsageStatus: input.UsageStatus, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey}); err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]bool{"reconciled": true})
}

func (handler *Handler) listPolicies(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, next, err := handler.service.ListPolicies(request.Context(), current.User, request.URL.Query().Get("cursor"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) createPolicy(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input PolicyInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	policy, err := handler.service.CreatePolicy(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(policy.Revision))
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"policy": policy})
}

func (handler *Handler) updatePolicy(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	revision, ok := auth.RequireRevision(response, request)
	if !ok {
		return
	}
	var input struct {
		LimitUnits int64  `json:"limit_units"`
		LimitUSD   string `json:"limit_usd"`
		Enabled    bool   `json:"enabled"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	policy, err := handler.service.UpdatePolicy(request.Context(), current.User, request.PathValue("id"), revision, input.LimitUnits, input.LimitUSD, input.Enabled)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	response.Header().Set("ETag", auth.ETag(policy.Revision))
	auth.WriteJSON(response, http.StatusOK, map[string]any{"policy": policy})
}

func (handler *Handler) effectiveLimits(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, err := handler.service.EffectiveLimits(request.Context(), current.User, request.PathValue("id"), request.URL.Query().Get("connection_id"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items})
}

func (handler *Handler) summary(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	summary, err := handler.service.Summary(request.Context(), current.User, UsageQuery{UserID: query.Get("user_id"), From: query.Get("from"), To: query.Get("to"), KeyID: query.Get("key_id"), ModelID: query.Get("model_id"), ConnectionID: query.Get("connection_id")})
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"usage": summary})
}

func (handler *Handler) unresolved(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	items, next, err := handler.service.Unresolved(request.Context(), current.User, UsageQuery{UserID: query.Get("user_id"), From: query.Get("from"), To: query.Get("to"), KeyID: query.Get("key_id"), ModelID: query.Get("model_id"), ConnectionID: query.Get("connection_id"), Cursor: query.Get("cursor")})
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) outbox(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	status, err := handler.service.OutboxStatus(request.Context(), current.User)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"outbox": status})
}

func (handler *Handler) listPrices(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.Authorize(response, request)
	if !ok {
		return
	}
	items, next, err := handler.service.ListPrices(request.Context(), current.User, request.URL.Query().Get("cursor"))
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"data": items, "next_cursor": next, "has_more": next != ""})
}

func (handler *Handler) createPrice(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input PriceInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	price, err := handler.service.CreatePrice(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusCreated, map[string]any{"price": price})
}

func (handler *Handler) previewReprice(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	var input RepriceInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	preview, err := handler.service.PreviewReprice(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"preview": preview})
}

func (handler *Handler) applyReprice(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if !requireRecentAuthentication(response, current, "repricing usage") {
		return
	}
	var input RepriceInput
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	result, err := handler.service.ApplyReprice(request.Context(), current.User, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]any{"result": result})
}

func (handler *Handler) adjust(response http.ResponseWriter, request *http.Request) {
	current, _, ok := handler.auth.AuthorizeMutation(response, request)
	if !ok {
		return
	}
	if !requireRecentAuthentication(response, current, "adjusting usage") {
		return
	}
	var input struct {
		AttemptID      string `json:"attempt_id"`
		DeltaUSD       string `json:"delta_usd"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !auth.DecodeJSON(response, request, &input) {
		return
	}
	cost, err := handler.service.AdjustCost(request.Context(), current.User, input.AttemptID, input.DeltaUSD, input.Reason, input.IdempotencyKey)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	auth.WriteJSON(response, http.StatusOK, map[string]string{"restated_cost_usd": cost})
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	var denial *Denial
	switch {
	case errors.As(err, &denial):
		if denial.RetryAfterSecond != nil {
			response.Header().Set("Retry-After", strconv.FormatInt(*denial.RetryAfterSecond, 10))
		}
		auth.WriteJSON(response, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": "limit_exceeded", "message": denial.Reason, "policy_id": denial.PolicyID, "metric": denial.Metric}})
	case errors.Is(err, ErrNotFound):
		auth.WriteError(response, http.StatusNotFound, "not_found", "Usage resource not found")
	case errors.Is(err, ErrDenied):
		auth.WriteError(response, http.StatusForbidden, "forbidden", "You do not have permission to perform this action")
	case errors.Is(err, ErrConflict):
		auth.WriteError(response, http.StatusConflict, "revision_conflict", "The resource changed; reload and try again")
	case strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "must") || strings.Contains(err.Error(), "overlap") || strings.Contains(err.Error(), "range") || strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "amount") || strings.Contains(err.Error(), "adjustment") || strings.Contains(err.Error(), "too large"):
		auth.WriteError(response, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		auth.WriteError(response, http.StatusServiceUnavailable, "usage_unavailable", "Usage and limits are unavailable")
	}
}

func requireRecentAuthentication(response http.ResponseWriter, current auth.AuthenticatedSession, action string) bool {
	if current.AuthenticatedAt < time.Now().Add(-15*time.Minute).UnixMilli() {
		auth.WriteError(response, http.StatusForbidden, "reauthentication_required", "Sign in again before "+action)
		return false
	}
	return true
}
