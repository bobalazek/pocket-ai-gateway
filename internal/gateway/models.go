package gateway

import (
	"net/http"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func (handler *Handler) geminiAction(response http.ResponseWriter, request *http.Request) {
	action := request.PathValue("action")
	colon := strings.LastIndex(action, ":")
	if colon < 1 {
		handler.writeError(response, "gemini", http.StatusNotFound, "NOT_FOUND", "Resource not found")
		return
	}
	publicID, name := strings.TrimPrefix(action[:colon], "models/"), action[colon+1:]
	scope := map[string]string{"generateContent": "chat:generate", "streamGenerateContent": "chat:generate", "countTokens": "tokens:count", "embedContent": "embeddings:generate", "batchEmbedContents": "embeddings:generate"}[name]
	if scope == "" {
		handler.writeError(response, "gemini", http.StatusNotFound, "NOT_FOUND", "Operation is unavailable")
		return
	}
	stream := name == "streamGenerateContent"
	handler.forward(response, request, "gemini", scope, name, publicID, &stream)
}

func (handler *Handler) authenticate(response http.ResponseWriter, request *http.Request, dialect string) (keys.Principal, bool) {
	var token string
	switch dialect {
	case "openai", "responses", "responses_compact":
		if len(request.Header.Values("Authorization")) != 1 {
			handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "Provide one API key")
			return keys.Principal{}, false
		}
		value := request.Header.Get("Authorization")
		if strings.HasPrefix(value, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
		}
	case "anthropic":
		if request.Header.Get("anthropic-version") != "2023-06-01" {
			handler.writeError(response, dialect, http.StatusBadRequest, "invalid_request_error", "anthropic-version must be 2023-06-01")
			return keys.Principal{}, false
		}
		if len(request.Header.Values("x-api-key")) != 1 {
			handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "Provide one API key")
			return keys.Principal{}, false
		}
		token = strings.TrimSpace(request.Header.Get("x-api-key"))
	case "gemini":
		header, query := strings.TrimSpace(request.Header.Get("x-goog-api-key")), request.URL.Query().Get("key")
		if len(request.Header.Values("x-goog-api-key")) > 1 || header != "" && query != "" {
			handler.writeError(response, dialect, http.StatusBadRequest, "INVALID_ARGUMENT", "Provide one API key")
			return keys.Principal{}, false
		}
		token = header
		if token == "" {
			token = query
		}
		queryValues := request.URL.Query()
		queryValues.Del("key")
		request.URL.RawQuery = queryValues.Encode()
	}
	if token == "" {
		handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "API key is required")
		return keys.Principal{}, false
	}
	principal, err := handler.keys.Authenticate(request.Context(), token)
	if err != nil {
		handler.writeError(response, dialect, http.StatusUnauthorized, "authentication_error", "API key is invalid")
		return keys.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) allowedModels(response http.ResponseWriter, request *http.Request, dialect string) (keys.Principal, []providers.PublicModel, bool) {
	principal, ok := handler.authenticate(response, request, dialect)
	if !ok {
		return keys.Principal{}, nil, false
	}
	items, err := handler.providers.ListPublicModels(request.Context())
	if err != nil {
		handler.writeError(response, dialect, http.StatusServiceUnavailable, "gateway_unavailable", "Models are unavailable")
		return keys.Principal{}, nil, false
	}
	filtered := items[:0]
	for _, item := range items {
		if handler.providers.HasAvailableRouteTarget(request.Context(), item.ID, func(connectionID string) bool { return principal.Allows("models:read", item.ID, connectionID) }, func(target providers.Target) bool { return modelVisibleInDialect(dialect, item, target) }) {
			filtered = append(filtered, item)
		}
	}
	return principal, filtered, true
}

func modelVisibleInDialect(dialect string, model providers.PublicModel, target providers.Target) bool {
	if hasCapability(model.Capabilities, "chat:generate") && hasCapability(target.UpstreamCapabilities, "chat:generate") {
		operation := map[string]string{"openai": "chat/completions", "openai_compatible": "chat/completions", "anthropic": "messages", "gemini": "generateContent"}[target.Adapter]
		return providers.PresetSupports(target.Preset, operation)
	}
	if !nativeAdapter(dialect, target.Adapter) {
		return false
	}
	operations := map[string][]struct{ scope, operation string }{
		"openai":    {{"completions:generate", "completions"}, {"embeddings:generate", "embeddings"}, {"moderations:classify", "moderations"}, {"tokens:count", "responses/input_tokens"}, {"images:generate", "images/generations"}, {"images:edit", "images/edits"}, {"images:variation", "images/variations"}, {"audio:speech", "audio/speech"}, {"audio:transcribe", "audio/transcriptions"}, {"audio:translate", "audio/translations"}},
		"anthropic": {{"tokens:count", "messages/count_tokens"}},
		"gemini":    {{"embeddings:generate", "embedContent"}, {"tokens:count", "countTokens"}, {"interactions:generate", "interactions"}},
	}[dialect]
	for _, candidate := range operations {
		if hasCapability(model.Capabilities, candidate.scope) && hasCapability(target.UpstreamCapabilities, candidate.scope) && providers.PresetSupports(target.Preset, candidate.operation) {
			return true
		}
	}
	return false
}
func (handler *Handler) openAIModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "openai")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"id": m.ID, "object": "model", "created": 0, "owned_by": "pocket-ai-gateway"})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}
func (handler *Handler) openAIModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "openai")
	if !ok {
		return
	}
	for _, m := range items {
		if m.ID == r.PathValue("model") {
			writeJSON(w, map[string]any{"id": m.ID, "object": "model", "created": 0, "owned_by": "pocket-ai-gateway"})
			return
		}
	}
	handler.writeError(w, "openai", http.StatusNotFound, "model_not_found", "Model is unavailable")
}
func (handler *Handler) anthropicModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "anthropic")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"id": m.ID, "type": "model", "display_name": m.Label, "created_at": "1970-01-01T00:00:00Z"})
	}
	first, last := "", ""
	if len(items) > 0 {
		first, last = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(w, map[string]any{"data": data, "has_more": false, "first_id": first, "last_id": last})
}
func (handler *Handler) anthropicModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "anthropic")
	if !ok {
		return
	}
	for _, m := range items {
		if m.ID == r.PathValue("model") {
			writeJSON(w, map[string]any{"id": m.ID, "type": "model", "display_name": m.Label, "created_at": "1970-01-01T00:00:00Z"})
			return
		}
	}
	handler.writeError(w, "anthropic", http.StatusNotFound, "not_found_error", "Model is unavailable")
}
func (handler *Handler) geminiModels(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "gemini")
	if !ok {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, m := range items {
		data = append(data, map[string]any{"name": "models/" + m.ID, "displayName": m.Label, "description": m.Description, "supportedGenerationMethods": geminiMethods(m.Capabilities)})
	}
	writeJSON(w, map[string]any{"models": data})
}
func (handler *Handler) geminiModel(w http.ResponseWriter, r *http.Request) {
	_, items, ok := handler.allowedModels(w, r, "gemini")
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.PathValue("model"), "models/")
	for _, m := range items {
		if m.ID == id {
			writeJSON(w, map[string]any{"name": "models/" + m.ID, "displayName": m.Label, "description": m.Description, "supportedGenerationMethods": geminiMethods(m.Capabilities)})
			return
		}
	}
	handler.writeError(w, "gemini", http.StatusNotFound, "NOT_FOUND", "Model is unavailable")
}

func geminiMethods(capabilities []string) []string {
	var out []string
	for _, v := range capabilities {
		switch v {
		case "chat", "generate_content":
			out = append(out, "generateContent")
		case "count_tokens":
			out = append(out, "countTokens")
		case "embeddings":
			out = append(out, "embedContent")
		case "interactions":
			out = append(out, "interactions")
		}
	}
	return out
}
