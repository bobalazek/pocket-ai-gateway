package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
)

const systemOneRequest = `{"model":"%s","state":"Help! My payouts have been failing for 3 days.","questions":{"is_urgent":{"type":"noul","instructions":"Does this convey urgency?","criteria":{"true":"Explicitly time-sensitive","false":"No urgency expressed"}},"team":{"type":"choice","instructions":"Route it","criteria":{"billing":"Payments","technical":"Bugs"}},"anger":{"type":"score","instructions":"How upset?","criteria":["Calm","Frustrated","Very angry"]}}}`

func TestSystemOneDecisionsForwardNativelyWithAccounting(t *testing.T) {
	var seenPath, seenAuth string
	var seenBody map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		w.Header().Set("Content-Type", "application/json")
		var state string
		_ = json.Unmarshal(seenBody["state"], &state)
		switch {
		case strings.Contains(state, "reject"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"detail":"questions.team: too many options for the context window"}`)
		case strings.Contains(state, "no usage"):
			io.WriteString(w, `{"model":"english","answers":{"is_urgent":{"type":"noul","noul":0.4}},"routing":{"model":"english"}}`)
		default:
			io.WriteString(w, `{"model":"jev-1.13.0","answers":{"is_urgent":{"type":"noul","noul":0.95},"team":{"type":"choice","choice":"billing","probabilities":{"billing":0.88,"technical":0.12},"confidence":0.81},"anger":{"type":"score","score":1.05,"legend":{"0":"Calm","1":"Frustrated","2":"Very angry"},"probabilities":{"0":0.0,"1":0.95,"2":0.05},"confidence":0.92}},"usage":{"input_tokens":296,"output_tokens":20}}`)
		}
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai_compatible", upstream.URL+"/v1", "jev-latest", []string{"decisions"})
	chatUpstream, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "chat-upstream", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := providerService.CreatePublicModel(ctx, owner, "chat-model", "Chat", "", chatUpstream.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "decisions", Scopes: []string{"decisions:generate", "chat:generate", "models:read"}, ModelPatterns: []string{model.ID, chatModel.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(method, path, body string) (*http.Response, string) {
		t.Helper()
		request, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response, string(raw)
	}
	attempt := func(requestID string) (string, string, int64, int64) {
		t.Helper()
		var state, status string
		var input, output int64
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if store.SystemDB().QueryRowContext(ctx, "SELECT state,usage_status,COALESCE(input_tokens,-1),COALESCE(output_tokens,-1) FROM attempts WHERE request_id=?", requestID).Scan(&state, &status, &input, &output) == nil && state != "dispatching" {
				break
			}
		}
		return state, status, input, output
	}

	response, body := call(http.MethodGet, "/api/systemone/v1/models", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"`+model.ID+`"`) || strings.Contains(body, chatModel.ID) {
		t.Fatalf("models = %d %s", response.StatusCode, body)
	}
	if response, body = call(http.MethodGet, "/api/openai/v1/models", ""); !strings.Contains(body, chatModel.ID) || strings.Contains(body, `"`+model.ID+`"`) {
		t.Fatalf("openai models = %d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodPost, "/api/systemone/v1/systemone", strings.Replace(systemOneRequest, "%s", model.ID, 1))
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"choice":"billing"`) {
		t.Fatalf("decision = %d %s", response.StatusCode, body)
	}
	if seenPath != "/v1/systemone" || seenAuth != "Bearer provider-secret" || string(seenBody["model"]) != `"jev-latest"` {
		t.Fatalf("upstream path=%s auth=%q model=%s", seenPath, seenAuth, seenBody["model"])
	}
	if state, status, input, output := attempt(response.Header.Get(pocketAIRequestIDHeader)); state != "succeeded" || status != "provider_reported" || input != 296 || output != 20 {
		t.Fatalf("accounting = %s %s %d/%d", state, status, input, output)
	}

	response, body = call(http.MethodPost, "/api/systemone/v1/systemone", strings.Replace(strings.Replace(systemOneRequest, "%s", model.ID, 1), "Help!", "no usage", 1))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("usage-less decision = %d %s", response.StatusCode, body)
	}
	if state, status, input, _ := attempt(response.Header.Get(pocketAIRequestIDHeader)); state != "succeeded" || status != "estimated" || input <= 0 {
		t.Fatalf("usage-less accounting = %s %s %d", state, status, input)
	}

	response, body = call(http.MethodPost, "/api/systemone/v1/systemone", strings.Replace(strings.Replace(systemOneRequest, "%s", model.ID, 1), "Help!", "reject", 1))
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"error_type":"validation_error"`) || !strings.Contains(body, "too many options") {
		t.Fatalf("upstream validation error = %d %s", response.StatusCode, body)
	}

	for name, invalid := range map[string]string{
		"missing state": `{"model":"` + model.ID + `","questions":{"a":{"type":"noul","instructions":"x"}}}`,
		"empty":         `{"model":"` + model.ID + `","state":"x","questions":{}}`,
		"bad type":      `{"model":"` + model.ID + `","state":"x","questions":{"a":{"type":"bool","instructions":"x"}}}`,
		"one level":     `{"model":"` + model.ID + `","state":"x","questions":{"a":{"type":"score","instructions":"x","criteria":["only"]}}}`,
		"no options":    `{"model":"` + model.ID + `","state":"x","questions":{"a":{"type":"choice","instructions":"x","criteria":{}}}}`,
		"stream":        `{"model":"` + model.ID + `","state":"x","stream":true,"questions":{"a":{"type":"noul","instructions":"x"}}}`,
	} {
		response, body = call(http.MethodPost, "/api/systemone/v1/systemone", invalid)
		if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"detail"`) {
			t.Fatalf("%s = %d %s", name, response.StatusCode, body)
		}
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/systemone/v1/systemone", strings.NewReader(strings.Replace(systemOneRequest, "%s", model.ID, 1)))
	unauthenticated, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", unauthenticated.StatusCode)
	}
}

func TestSystemOneRequiresDecisionCapability(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream call to %s", r.URL.Path)
	}))
	defer upstream.Close()
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	connection, model := publishModel(t, ctx, providerService, owner, "openai_compatible", upstream.URL+"/v1", "chat-model", []string{"chat"})
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "decisions", Scopes: []string{"decisions:generate", "chat:generate"}, ModelPatterns: []string{model.ID}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(store.SystemDB(), keyService, providerService, usageService).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/systemone/v1/systemone", strings.NewReader(strings.Replace(systemOneRequest, "%s", model.ID, 1)))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"detail"`) {
		t.Fatalf("chat-only model = %d %s", response.StatusCode, body)
	}
}
