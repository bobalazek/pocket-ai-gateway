package mediajobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

func createGeminiVideo(ctx context.Context, target providers.Target, input json.RawMessage) (providerPrediction, error) {
	if !safeSegment(target.UpstreamID) {
		return providerPrediction{}, errors.New("invalid Gemini video model ID")
	}
	body, err := geminiVideoRequest(input)
	if err != nil {
		return providerPrediction{}, err
	}
	return callGeminiVideo(ctx, target, http.MethodPost, "models/"+target.UpstreamID+":predictLongRunning", body)
}

func getGeminiVideo(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	if !safeProviderPath(id) {
		return providerPrediction{}, errors.New("invalid Gemini operation ID")
	}
	return callGeminiVideo(ctx, target, http.MethodGet, id, nil)
}

func geminiVideoRequest(input json.RawMessage) ([]byte, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(input, &object) != nil || object == nil {
		return nil, ErrInvalid
	}
	if instances := object["instances"]; len(instances) > 0 {
		var values []json.RawMessage
		if json.Unmarshal(instances, &values) != nil || len(values) == 0 {
			return nil, ErrInvalid
		}
		return json.Marshal(object)
	}
	parameters := object["parameters"]
	delete(object, "parameters")
	if len(object) == 0 {
		return nil, ErrInvalid
	}
	instance, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	request := map[string]json.RawMessage{"instances": json.RawMessage("[" + string(instance) + "]")}
	if len(parameters) > 0 {
		request["parameters"] = parameters
	}
	return json.Marshal(request)
}

func callGeminiVideo(ctx context.Context, target providers.Target, method, relative string, body []byte) (providerPrediction, error) {
	endpoint, err := joinURL(target.BaseURL, relative)
	if err != nil {
		return providerPrediction{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return providerPrediction{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-goog-api-key", target.Credential)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := safeClient(time.Duration(target.TimeoutMS)*time.Millisecond, target.AllowPrivateNetwork).Do(request)
	if err != nil {
		return providerPrediction{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOutputBytes+1))
	if err != nil {
		return providerPrediction{}, err
	}
	if len(raw) > maxOutputBytes {
		return providerPrediction{}, errors.New("provider response exceeds 4 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerPrediction{}, providerHTTPError(response.StatusCode)
	}
	var operation struct {
		Name     string          `json:"name"`
		Done     bool            `json:"done"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &operation) != nil || !safeProviderPath(operation.Name) {
		return providerPrediction{}, errors.New("provider returned an invalid video operation")
	}
	prediction := providerPrediction{ID: operation.Name, Status: "processing", Output: json.RawMessage(`null`)}
	if !operation.Done {
		return prediction, nil
	}
	if len(bytes.TrimSpace(operation.Error)) > 0 && !bytes.Equal(bytes.TrimSpace(operation.Error), []byte("null")) {
		prediction.Status = "failed"
		return prediction, nil
	}
	if len(operation.Response) == 0 || !json.Valid(operation.Response) {
		return providerPrediction{}, errors.New("provider returned an invalid video result")
	}
	prediction.Status, prediction.Output = "succeeded", operation.Response
	return prediction, nil
}

func safeProviderPath(value string) bool {
	if value == "" || len(value) > 500 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !safeSegment(segment) {
			return false
		}
	}
	return true
}
