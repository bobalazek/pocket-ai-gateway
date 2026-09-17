package mediajobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

type providerPrediction struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
	CostNanos *int64          `json:"-"`
}

type providerHTTPError int

func (status providerHTTPError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d", status)
}

func retryableProviderError(err error) bool {
	var status providerHTTPError
	if errors.As(err, &status) {
		return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func createReplicatePrediction(ctx context.Context, target providers.Target, input json.RawMessage) (providerPrediction, error) {
	path, body, err := replicateCreateRequest(target.UpstreamID, input)
	if err != nil {
		return providerPrediction{}, err
	}
	return callReplicate(ctx, target, http.MethodPost, path, body)
}

func getReplicatePrediction(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	if !safeSegment(id) {
		return providerPrediction{}, errors.New("invalid provider job ID")
	}
	return callReplicate(ctx, target, http.MethodGet, "predictions/"+id, nil)
}

func cancelReplicatePrediction(ctx context.Context, target providers.Target, id string) (providerPrediction, error) {
	if !safeSegment(id) {
		return providerPrediction{}, errors.New("invalid provider job ID")
	}
	return callReplicate(ctx, target, http.MethodPost, "predictions/"+id+"/cancel", []byte(`{}`))
}

func replicateCreateRequest(model string, input json.RawMessage) (string, []byte, error) {
	request := map[string]json.RawMessage{"input": input}
	path := "predictions"
	switch {
	case strings.HasPrefix(model, "version:"):
		version := strings.TrimPrefix(model, "version:")
		if !safeSegment(version) {
			return "", nil, errors.New("invalid Replicate version model ID")
		}
		encoded, _ := json.Marshal(version)
		request["version"] = encoded
	case strings.HasPrefix(model, "deployment:"):
		owner, name, ok := splitReplicateModel(strings.TrimPrefix(model, "deployment:"))
		if !ok {
			return "", nil, errors.New("invalid Replicate deployment model ID")
		}
		path = "deployments/" + owner + "/" + name + "/predictions"
	default:
		owner, name, ok := splitReplicateModel(model)
		if !ok {
			return "", nil, errors.New("invalid Replicate model ID")
		}
		path = "models/" + owner + "/" + name + "/predictions"
	}
	body, err := json.Marshal(request)
	return path, body, err
}

func callReplicate(ctx context.Context, target providers.Target, method, relative string, body []byte) (providerPrediction, error) {
	endpoint, err := joinURL(target.BaseURL, relative)
	if err != nil {
		return providerPrediction{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return providerPrediction{}, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+target.Credential)
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
	var prediction providerPrediction
	if err := json.Unmarshal(raw, &prediction); err != nil || prediction.ID == "" || !safeSegment(prediction.ID) {
		return providerPrediction{}, errors.New("provider returned an invalid prediction")
	}
	if len(prediction.Output) == 0 {
		prediction.Output = json.RawMessage(`null`)
	}
	if !json.Valid(prediction.Output) {
		return providerPrediction{}, errors.New("provider returned invalid output")
	}
	return prediction, nil
}

func splitReplicateModel(value string) (string, string, bool) {
	parts := strings.Split(value, "/")
	returnValue := len(parts) == 2 && safeSegment(parts[0]) && safeSegment(parts[1])
	if !returnValue {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func safeSegment(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func joinURL(base, relative string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(relative)
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.User != nil || reference.Fragment != "" {
		return "", errors.New("invalid provider path")
	}
	basePath, baseEscapedPath := parsed.Path, parsed.EscapedPath()
	parsed.Path = strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(reference.Path, "/")
	parsed.RawPath = strings.TrimRight(baseEscapedPath, "/") + "/" + strings.TrimLeft(reference.EscapedPath(), "/")
	parsed.RawQuery = reference.RawQuery
	return parsed.String(), nil
}

func safeClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: min(timeout, 10*time.Second)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if allowPrivate || ip.IsGlobalUnicast() && !ip.IsPrivate() {
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
		}
		return nil, errors.New("provider destination is not allowed")
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects are disabled") }}
}
