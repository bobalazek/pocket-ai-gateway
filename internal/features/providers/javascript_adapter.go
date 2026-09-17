package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	maxAdapterScriptBytes = 64 << 10
	maxAdapterOutputBytes = 16 << 20
	adapterExecutionLimit = 50 * time.Millisecond
)

var ErrAdapterTransform = errors.New("provider adapter transform failed")

type AdapterScript struct {
	ConnectionID   string `json:"connection_id"`
	RequestScript  string `json:"request_script"`
	ResponseScript string `json:"response_script"`
	Revision       int64  `json:"revision"`
	UpdatedAt      string `json:"updated_at"`
}

type AdapterScriptInput struct {
	RequestScript  string `json:"request_script"`
	ResponseScript string `json:"response_script"`
}

type AdapterRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

type AdapterResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
}

func validateAdapterScriptInput(input AdapterScriptInput) (AdapterScriptInput, error) {
	input.RequestScript = strings.TrimSpace(input.RequestScript)
	input.ResponseScript = strings.TrimSpace(input.ResponseScript)
	if input.RequestScript == "" && input.ResponseScript == "" {
		return AdapterScriptInput{}, errors.New("request_script or response_script is required")
	}
	for _, item := range []struct{ name, script string }{{"request_script", input.RequestScript}, {"response_script", input.ResponseScript}} {
		name, script := item.name, item.script
		if len(script) > maxAdapterScriptBytes {
			return AdapterScriptInput{}, fmt.Errorf("%s is too large", name)
		}
		if script != "" {
			if err := validateTransformFunction(script); err != nil {
				return AdapterScriptInput{}, fmt.Errorf("%s must be a valid function expression", name)
			}
		}
	}
	return input, nil
}

func ValidateAdapterScriptInput(input AdapterScriptInput) (AdapterScriptInput, error) {
	return validateAdapterScriptInput(input)
}

func validateTransformFunction(script string) error {
	_, err := runTransform(context.Background(), script, map[string]any{}, false)
	return err
}

func TransformAdapterRequest(ctx context.Context, script string, operation, model string, request AdapterRequest) (AdapterRequest, error) {
	if script == "" {
		return request, nil
	}
	if len(request.Body) > maxAdapterOutputBytes {
		return AdapterRequest{}, ErrAdapterTransform
	}
	body, err := decodeAdapterJSON(request.Body)
	if err != nil {
		return AdapterRequest{}, ErrAdapterTransform
	}
	result, err := runTransform(ctx, script, map[string]any{
		"operation": operation,
		"model":     model,
		"method":    request.Method,
		"path":      request.Path,
		"headers":   headerMap(request.Headers),
		"body":      body,
	}, true)
	if err != nil {
		return AdapterRequest{}, ErrAdapterTransform
	}
	if err := applyAdapterRequestResult(&request, result); err != nil {
		return AdapterRequest{}, ErrAdapterTransform
	}
	return request, nil
}

func TransformAdapterResponse(ctx context.Context, script string, operation, model string, response AdapterResponse) (AdapterResponse, error) {
	if script == "" {
		return response, nil
	}
	if len(response.Body) > maxAdapterOutputBytes {
		return AdapterResponse{}, ErrAdapterTransform
	}
	body, err := decodeAdapterJSON(response.Body)
	if err != nil {
		return AdapterResponse{}, ErrAdapterTransform
	}
	result, err := runTransform(ctx, script, map[string]any{
		"operation": operation,
		"model":     model,
		"status":    response.Status,
		"headers":   headerMap(response.Headers),
		"body":      body,
	}, true)
	if err != nil {
		return AdapterResponse{}, ErrAdapterTransform
	}
	if err := applyAdapterResponseResult(&response, result); err != nil {
		return AdapterResponse{}, ErrAdapterTransform
	}
	return response, nil
}

func runTransform(ctx context.Context, script string, input any, invoke bool) (map[string]json.RawMessage, error) {
	if len(script) > maxAdapterScriptBytes {
		return nil, errors.New("adapter script is too large")
	}
	program, err := goja.Compile("provider-adapter.js", "("+script+")", true)
	if err != nil {
		return nil, err
	}
	runtime := goja.New()
	runtime.SetMaxCallStackSize(64)
	timedOut := errors.New("adapter execution limit exceeded")
	timer := time.AfterFunc(adapterExecutionLimit, func() { runtime.Interrupt(timedOut) })
	stopContext := context.AfterFunc(ctx, func() { runtime.Interrupt(ctx.Err()) })
	defer timer.Stop()
	defer stopContext()
	value, err := runtime.RunProgram(program)
	if err != nil {
		return nil, err
	}
	transform, ok := goja.AssertFunction(value)
	if !ok {
		return nil, errors.New("adapter script is not a function")
	}
	if !invoke {
		return map[string]json.RawMessage{}, nil
	}
	value, err = transform(goja.Undefined(), runtime.ToValue(input))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value.Export())
	if err != nil || len(raw) > maxAdapterOutputBytes {
		return nil, errors.New("adapter output is invalid")
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, errors.New("adapter output must be an object")
	}
	return result, nil
}

func applyAdapterRequestResult(request *AdapterRequest, result map[string]json.RawMessage) error {
	for name := range result {
		if name != "method" && name != "path" && name != "headers" && name != "body" {
			return errors.New("unknown request field")
		}
	}
	if raw, ok := result["method"]; ok {
		if err := json.Unmarshal(raw, &request.Method); err != nil {
			return err
		}
		request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
		if request.Method != http.MethodGet && request.Method != http.MethodPost && request.Method != http.MethodPatch && request.Method != http.MethodDelete {
			return errors.New("unsupported request method")
		}
	}
	if raw, ok := result["path"]; ok {
		if err := json.Unmarshal(raw, &request.Path); err != nil || !validAdapterPath(request.Path) {
			return errors.New("invalid request path")
		}
	}
	if raw, ok := result["headers"]; ok {
		var headers map[string]string
		if err := json.Unmarshal(raw, &headers); err != nil {
			return err
		}
		for name, value := range headers {
			if !validAdapterHeader(name, value) {
				return errors.New("invalid request header")
			}
			request.Headers.Set(name, value)
		}
	}
	if raw, ok := result["body"]; ok {
		if len(raw) > maxAdapterOutputBytes || !json.Valid(raw) {
			return errors.New("invalid request body")
		}
		request.Body = append([]byte(nil), raw...)
	}
	return nil
}

func applyAdapterResponseResult(response *AdapterResponse, result map[string]json.RawMessage) error {
	for name := range result {
		if name != "status" && name != "headers" && name != "body" {
			return errors.New("unknown response field")
		}
	}
	if raw, ok := result["status"]; ok {
		if err := json.Unmarshal(raw, &response.Status); err != nil || response.Status < 200 || response.Status > 299 {
			return errors.New("invalid response status")
		}
	}
	if raw, ok := result["headers"]; ok {
		var headers map[string]string
		if err := json.Unmarshal(raw, &headers); err != nil {
			return err
		}
		for name, value := range headers {
			if !validAdapterHeader(name, value) {
				return errors.New("invalid response header")
			}
			response.Headers.Set(name, value)
		}
	}
	if raw, ok := result["body"]; ok {
		if len(raw) > maxAdapterOutputBytes || !json.Valid(raw) {
			return errors.New("invalid response body")
		}
		response.Body = append([]byte(nil), raw...)
	}
	return nil
}

func decodeAdapterJSON(raw []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func validAdapterPath(value string) bool {
	if value == "" || len(value) > 2048 {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "" && parsed.Host == "" && parsed.User == nil && parsed.Fragment == ""
}

func validAdapterHeader(name, value string) bool {
	name = http.CanonicalHeaderKey(strings.TrimSpace(name))
	if name == "" || len(name) > 100 || len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	switch name {
	case "Authorization", "Cookie", "Host", "Connection", "Content-Length", "Proxy-Authorization", "Set-Cookie", "Transfer-Encoding", "Upgrade", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto":
		return false
	}
	return true
}

func headerMap(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) > 0 && validAdapterHeader(name, values[0]) {
			result[name] = values[0]
		}
	}
	return result
}
