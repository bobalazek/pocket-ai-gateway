package auth

import "testing"

func TestOpenAICompletionsScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("completions:generate") {
		t.Fatal("completions:generate was rejected")
	}
}

func TestOpenAIRealtimeScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("realtime:connect") {
		t.Fatal("realtime:connect was rejected")
	}
}

func TestAnthropicWebSearchScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:web_search") {
		t.Fatal("messages:web_search was rejected")
	}
}

func TestAnthropicWebFetchScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:web_fetch") {
		t.Fatal("messages:web_fetch was rejected")
	}
}

func TestAnthropicMessageBatchesScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:batches") {
		t.Fatal("messages:batches was rejected")
	}
}

func TestOpenAIBatchesScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("batches:manage") {
		t.Fatal("batches:manage must be accepted as an inference scope")
	}
}

func TestOpenAIFilesScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("files:manage") {
		t.Fatal("files:manage was rejected")
	}
}

func TestOpenAIVectorStoresScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("vector_stores:manage") {
		t.Fatal("vector_stores:manage was rejected")
	}
}

func TestOpenAIResponsesFileSearchScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("responses:file_search") {
		t.Fatal("responses:file_search was rejected")
	}
	for _, scope := range inferenceScopes {
		if scope.ID == "responses:file_search" && scope.Policy != "" {
			return
		}
	}
	t.Fatal("responses:file_search policy is missing")
}
