package auth

type InferenceScope struct {
	ID     string `json:"id"`
	Policy string `json:"policy,omitempty"`
}

var inferenceScopes = []InferenceScope{
	{ID: "chat:generate"},
	{ID: "completions:generate", Policy: "Required for legacy OpenAI Completions."},
	{ID: "messages:batches", Policy: "Anthropic Message Batches also require chat:generate."},
	{ID: "messages:web_search", Policy: "Anthropic web search also requires chat:generate."},
	{ID: "messages:web_fetch", Policy: "Anthropic web fetch also requires chat:generate."},
	{ID: "responses:generate"},
	{ID: "responses:web_search", Policy: "OpenAI Responses web search also requires responses:generate."},
	{ID: "responses:file_search", Policy: "OpenAI Responses file search also requires responses:generate."},
	{ID: "embeddings:generate"},
	{ID: "moderations:classify"},
	{ID: "images:generate"},
	{ID: "images:edit"},
	{ID: "images:variation"},
	{ID: "audio:speech"},
	{ID: "audio:transcribe"},
	{ID: "audio:translate"},
	{ID: "realtime:connect", Policy: "OpenAI Realtime WebSocket sessions use this scope."},
	{ID: "media:generate", Policy: "Create and manage provider-neutral asynchronous media jobs."},
	{ID: "batches:manage"},
	{ID: "files:manage"},
	{ID: "vector_stores:manage"},
	{ID: "models:read"},
	{ID: "tokens:count"},
}

func ValidInferenceScope(scope string) bool {
	for _, candidate := range inferenceScopes {
		if candidate.ID == scope {
			return true
		}
	}
	return false
}
