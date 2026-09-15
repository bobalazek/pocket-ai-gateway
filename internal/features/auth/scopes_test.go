package auth

import "testing"

func TestAnthropicWebSearchScopeIsAnInferenceScope(t *testing.T) {
	if !ValidInferenceScope("messages:web_search") {
		t.Fatal("messages:web_search was rejected")
	}
}
