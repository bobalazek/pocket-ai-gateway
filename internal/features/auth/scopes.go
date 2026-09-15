package auth

var inferenceScopes = map[string]struct{}{
	"chat:generate": {}, "messages:batches": {}, "messages:web_search": {}, "responses:generate": {}, "responses:web_search": {}, "embeddings:generate": {},
	"models:read": {}, "tokens:count": {}, "moderations:classify": {}, "images:generate": {}, "images:edit": {}, "images:variation": {}, "audio:speech": {}, "audio:transcribe": {}, "audio:translate": {},
}

func ValidInferenceScope(scope string) bool {
	_, ok := inferenceScopes[scope]
	return ok
}
