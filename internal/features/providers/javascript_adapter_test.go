package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestJavaScriptAdapterTransformsJSONWithoutHostCapabilities(t *testing.T) {
	request, err := TransformAdapterRequest(context.Background(), `(input) => ({
		method: "POST",
		path: "predictions?wait=1",
		headers: {"Prefer": "wait=1"},
		body: {version: input.model, input: {prompt: input.body.messages[0].content}, sandbox: [typeof fetch, typeof require, typeof process]}
	})`, "chat/completions", "owner/model", AdapterRequest{
		Method:  http.MethodPost,
		Path:    "chat/completions",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"messages":[{"content":"draw a fox"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Path != "predictions?wait=1" || request.Headers.Get("Prefer") != "wait=1" || string(request.Body) != `{"input":{"prompt":"draw a fox"},"sandbox":["undefined","undefined","undefined"],"version":"owner/model"}` {
		t.Fatalf("transformed request = %#v body=%s", request, request.Body)
	}

	response, err := TransformAdapterResponse(context.Background(), `(input) => ({status: 200, headers: {"Content-Type": "application/json"}, body: {id: input.body.id, object: "media.job", status: input.body.status}})`, "media/jobs", "owner/model", AdapterResponse{
		Status:  201,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"id":"job_1","status":"starting"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || string(response.Body) != `{"id":"job_1","object":"media.job","status":"starting"}` {
		t.Fatalf("transformed response = %#v body=%s", response, response.Body)
	}
}

func TestJavaScriptAdapterRejectsUnsafeOutputAndStopsRunawayCode(t *testing.T) {
	_, err := TransformAdapterRequest(context.Background(), `(input) => ({headers: {Authorization: "stolen"}})`, "chat/completions", "model", AdapterRequest{Method: http.MethodPost, Path: "chat/completions", Headers: make(http.Header), Body: []byte(`{}`)})
	if !errors.Is(err, ErrAdapterTransform) {
		t.Fatalf("unsafe header error = %v", err)
	}

	started := time.Now()
	_, err = TransformAdapterRequest(context.Background(), `() => { while (true) {} }`, "chat/completions", "model", AdapterRequest{Method: http.MethodPost, Path: "chat/completions", Headers: make(http.Header), Body: []byte(`{}`)})
	if !errors.Is(err, ErrAdapterTransform) || time.Since(started) > time.Second {
		t.Fatalf("runaway script error=%v duration=%v", err, time.Since(started))
	}
}
