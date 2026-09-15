package auth

import "testing"

func TestAnthropicWebSearchScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:web_search") {
		t.Fatal("messages:web_search was rejected")
	}
}

func TestAnthropicMessageBatchesScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:batches") {
		t.Fatal("messages:batches was rejected")
	}
}

func TestOpenAIFilesScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("files:manage") {
		t.Fatal("files:manage was rejected")
	}
}
