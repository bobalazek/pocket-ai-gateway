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
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

func createTogetherVideo(ctx context.Context, target providers.Target, input json.RawMessage) (providerPrediction, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(input, &body) != nil || body == nil {
		return providerPrediction{}, ErrInvalid
	}
	model, _ := json.Marshal(target.UpstreamID)
	body["model"] = model
	encoded, err := json.Marshal(body)
	if err != nil {
		return providerPrediction{}, err
	}
	return callTogether(ctx, target, http.MethodPost, "videos", encoded)
}

func getTogetherVideo(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	if !safeSegment(id) {
		return providerPrediction{}, errors.New("invalid provider job ID")
	}
	return callTogether(ctx, target, http.MethodGet, "videos/"+id, nil)
}

func callTogether(ctx context.Context, target providers.Target, method, relative string, body []byte) (providerPrediction, error) {
	endpoint, err := joinURL(target.BaseURL, relative)
	if err != nil {
		return providerPrediction{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return providerPrediction{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+target.Credential)
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
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return providerPrediction{}, errors.New("provider returned an invalid video job")
	}
	var prediction providerPrediction
	_ = json.Unmarshal(envelope["id"], &prediction.ID)
	_ = json.Unmarshal(envelope["status"], &prediction.Status)
	if prediction.ID == "" || !safeSegment(prediction.ID) || prediction.Status == "" {
		return providerPrediction{}, errors.New("provider returned an invalid video job")
	}
	if output := envelope["output"]; len(output) > 0 {
		prediction.Output = output
	} else if outputs := envelope["outputs"]; len(outputs) > 0 {
		prediction.Output = outputs
	} else if prediction.Status == "completed" {
		prediction.Output = raw
	} else {
		prediction.Output = json.RawMessage(`null`)
	}
	prediction.CostNanos = togetherCost(envelope)
	return prediction, nil
}

func togetherCost(envelope map[string]json.RawMessage) *int64 {
	raw := envelope["cost"]
	if len(raw) == 0 {
		for _, field := range []string{"output", "outputs"} {
			var nested map[string]json.RawMessage
			if json.Unmarshal(envelope[field], &nested) == nil && nested != nil && len(nested["cost"]) > 0 {
				raw = nested["cost"]
				break
			}
		}
	}
	if len(raw) == 0 {
		return nil
	}
	value := strings.Trim(string(raw), `"`)
	cost, err := usage.ParseUSD(value)
	if err != nil {
		return nil
	}
	return &cost
}
