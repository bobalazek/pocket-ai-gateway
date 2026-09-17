package mediajobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const customMediaOperation = "media/jobs"

func createCustomMediaJob(ctx context.Context, target providers.Target, input json.RawMessage) (providerPrediction, error) {
	return callCustomMediaJob(ctx, target, providers.AdapterRequest{
		Method:  http.MethodPost,
		Path:    customMediaOperation,
		Headers: make(http.Header),
		Body:    input,
	})
}

func getCustomMediaJob(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	return customMediaJobAction(ctx, target, id, "get")
}

func cancelCustomMediaJob(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	return customMediaJobAction(ctx, target, id, "cancel")
}

func customMediaJobAction(ctx context.Context, target providers.Target, id, action string) (providerPrediction, error) {
	if !validCustomJobID(id) {
		return providerPrediction{}, errors.New("invalid provider job ID")
	}
	body, err := json.Marshal(map[string]string{"action": action, "id": id})
	if err != nil {
		return providerPrediction{}, err
	}
	path := customMediaOperation + "/" + url.PathEscape(id)
	method := http.MethodGet
	if action == "cancel" {
		method, path = http.MethodPost, path+"/cancel"
	}
	return callCustomMediaJob(ctx, target, providers.AdapterRequest{Method: method, Path: path, Headers: make(http.Header), Body: body})
}

func callCustomMediaJob(ctx context.Context, target providers.Target, adapterRequest providers.AdapterRequest) (providerPrediction, error) {
	if strings.TrimSpace(target.AdapterRequestScript) == "" {
		return providerPrediction{}, errors.New("custom media jobs require a request adapter script")
	}
	adapterRequest.Headers.Set("Accept", "application/json")
	adapterRequest.Headers.Set("Content-Type", "application/json")
	adapterRequest, err := providers.TransformAdapterRequest(ctx, target.AdapterRequestScript, customMediaOperation, target.UpstreamID, adapterRequest)
	if err != nil {
		return providerPrediction{}, err
	}
	endpoint, err := joinURL(target.BaseURL, adapterRequest.Path)
	if err != nil {
		return providerPrediction{}, err
	}
	request, err := http.NewRequestWithContext(ctx, adapterRequest.Method, endpoint, bytes.NewReader(adapterRequest.Body))
	if err != nil {
		return providerPrediction{}, err
	}
	request.Header = adapterRequest.Headers.Clone()
	if target.Credential != "" {
		request.Header.Set("Authorization", "Bearer "+target.Credential)
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
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	adapterResponse, err := providers.TransformAdapterResponse(ctx, target.AdapterResponseScript, customMediaOperation, target.UpstreamID, providers.AdapterResponse{Status: response.StatusCode, Headers: response.Header.Clone(), Body: raw})
	if err != nil {
		return providerPrediction{}, err
	}
	if adapterResponse.Status < 200 || adapterResponse.Status >= 300 {
		return providerPrediction{}, providerHTTPError(adapterResponse.Status)
	}
	return decodeCustomMediaPrediction(adapterResponse.Body)
}

func decodeCustomMediaPrediction(raw []byte) (providerPrediction, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return providerPrediction{}, errors.New("custom provider returned an invalid media job")
	}
	var prediction providerPrediction
	_ = json.Unmarshal(envelope["id"], &prediction.ID)
	_ = json.Unmarshal(envelope["status"], &prediction.Status)
	if !validCustomJobID(prediction.ID) || prediction.Status == "" {
		return providerPrediction{}, errors.New("custom provider returned an invalid media job")
	}
	if output := envelope["output"]; len(output) > 0 {
		prediction.Output = output
	} else {
		prediction.Output = json.RawMessage(`null`)
	}
	if rawCost := envelope["cost_usd"]; len(rawCost) > 0 {
		var value string
		if json.Unmarshal(rawCost, &value) != nil {
			value = strings.Trim(string(rawCost), `"`)
		}
		if cost, err := usage.ParseUSD(value); err == nil {
			prediction.CostNanos = &cost
		}
	}
	return prediction, nil
}

func validCustomJobID(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 500 && !strings.ContainsAny(value, "\r\n\x00")
}
