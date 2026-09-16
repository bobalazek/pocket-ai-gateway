package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateOpenAICompletionPromptAndCandidateCardinality(t *testing.T) {
	for name, test := range map[string]struct {
		body             string
		prompts, choices int64
	}{
		"null":                {`{"model":"m","prompt":null}`, 1, 1},
		"string":              {`{"model":"m","prompt":"hello"}`, 1, 1},
		"strings":             {`{"model":"m","prompt":["one","two"],"n":3}`, 2, 3},
		"tokens":              {`{"model":"m","prompt":[1,2,3],"best_of":4,"n":2}`, 1, 4},
		"token arrays":        {`{"model":"m","prompt":[[1],[2,3]],"max_tokens":8}`, 2, 1},
		"stream options":      {`{"model":"m","prompt":"hello","stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false}}`, 1, 1},
		"null stream options": {`{"model":"m","prompt":"hello","stream_options":null}`, 1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.body), &envelope); err != nil {
				t.Fatal(err)
			}
			request, err := ValidateOpenAICompletion(envelope)
			if err != nil || request.PromptCount != test.prompts || request.Candidates != test.choices {
				t.Fatalf("request=%#v error=%v", request, err)
			}
		})
	}
}

func TestValidateOpenAICompletionRejectsUnsafeOrUnsupportedRequests(t *testing.T) {
	invalid := []string{
		`{"model":"m"}`,
		`{"model":"m","prompt":[]}`,
		`{"model":"m","prompt":[1,"two"]}`,
		`{"model":"m","prompt":"x","n":0}`,
		`{"model":"m","prompt":"x","best_of":1,"n":2}`,
		`{"model":"m","prompt":"x","best_of":2,"n":2}`,
		`{"model":"m","prompt":"x","stream":true,"best_of":2}`,
		`{"model":"m","prompt":"x","stream_options":{"include_usage":true}}`,
		`{"model":"m","prompt":"x","logprobs":6}`,
		`{"model":"m","prompt":"x","stop":["1","2","3","4","5"]}`,
		`{"model":"m","prompt":"x","temperature":3}`,
		`{"model":"m","prompt":"x","logit_bias":{"token":1}}`,
		`{"model":"m","prompt":"x","logit_bias":{"-1":1}}`,
		`{"model":"m","prompt":"x","messages":[]}`,
	}
	for index, body := range invalid {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(body), &envelope)
		if _, err := ValidateOpenAICompletion(envelope); err == nil {
			t.Fatalf("invalid request %d was accepted", index)
		}
	}
}

func TestNormalizeOpenAICompletionResponseValidatesRequiredFields(t *testing.T) {
	valid := `{"id":"cmpl_1","created":1,"model":"upstream","object":"text_completion","choices":[{"text":"Hi","index":0,"logprobs":null,"finish_reason":"stop"}]}`
	normalized, err := NormalizeOpenAICompletionResponse([]byte(valid), "public")
	if err != nil || !bytes.Contains(normalized, []byte(`"model":"public"`)) {
		t.Fatalf("normalized=%s error=%v", normalized, err)
	}

	invalid := []string{
		`not-json`,
		`{"created":1,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":null,"created":1,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":"","created":1,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":"c","created":1.5,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":"c","created":null,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":"c","created":-1,"model":"m","object":"text_completion","choices":[]}`,
		`{"id":"c","created":1,"object":"text_completion","choices":[]}`,
		`{"id":"c","created":1,"model":null,"object":"text_completion","choices":[]}`,
		`{"id":"c","created":1,"model":"m","object":"chat.completion","choices":[]}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":null}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":[{"index":0,"logprobs":null,"finish_reason":"stop"}]}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":[{"text":"x","index":-1,"logprobs":null,"finish_reason":"stop"}]}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":[{"text":"x","index":0,"logprobs":[],"finish_reason":"stop"}]}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":[{"text":"x","index":0,"logprobs":null,"finish_reason":null}]}`,
		`{"id":"c","created":1,"model":"m","object":"text_completion","choices":[{"text":"x","index":0,"logprobs":null,"finish_reason":"tool_calls"}]}`,
	}
	for index, body := range invalid {
		if _, err := NormalizeOpenAICompletionResponse([]byte(body), "public"); !errors.Is(err, ErrInvalidOpenAICompletion) {
			t.Fatalf("invalid response %d error=%v", index, err)
		}
	}
}

func TestCopyOpenAICompletionStreamRewritesEveryModelAndRequiresDone(t *testing.T) {
	source := "data: {\"id\":\"cmpl_1\",\"created\":1,\"object\":\"text_completion\",\"model\":\"upstream\",\"choices\":[{\"text\":\"Hi\",\"index\":0,\"logprobs\":null,\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"cmpl_1\",\"created\":1,\"object\":\"text_completion\",\"model\":\"upstream\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	var output bytes.Buffer
	if err := CopyOpenAICompletionStream(&output, strings.NewReader(source), "public"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), `"model":"public"`) != 2 || !strings.HasSuffix(output.String(), "data: [DONE]\n\n") {
		t.Fatalf("rewritten stream=%q", output.String())
	}
	if err := CopyOpenAICompletionStream(&bytes.Buffer{}, strings.NewReader(strings.TrimSuffix(source, "data: [DONE]\n\n")), "public"); !errors.Is(err, ErrInvalidOpenAICompletion) {
		t.Fatalf("missing DONE error=%v", err)
	}
}
