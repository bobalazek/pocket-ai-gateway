package providers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestRouteStrategiesFreePolicyAndCircuit(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	first := routeFixture(t, ctx, service, owner, "first")
	second := routeFixture(t, ctx, service, owner, "second")
	model, err := service.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", first.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConfigureRoute(ctx, owner, model.ID, model.Revision, RouteConfigInput{Strategy: "fixed", Targets: []RouteTargetInput{{UpstreamModelID: first.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: second.ID, Priority: 2, Weight: 1, Enabled: true}}}); err == nil {
		t.Fatal("fixed route accepted multiple targets")
	}
	embedding, err := service.CreatePublicModel(ctx, owner, "vectors", "Vectors", "", first.ID, []string{"embeddings"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConfigureRoute(ctx, owner, embedding.ID, embedding.Revision, RouteConfigInput{Strategy: "weighted", Targets: []RouteTargetInput{{UpstreamModelID: first.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: second.ID, Priority: 2, Weight: 1, Enabled: true}}}); err == nil {
		t.Fatal("embedding route accepted adaptive targets")
	}
	model, err = service.ConfigureRoute(ctx, owner, model.ID, model.Revision, RouteConfigInput{Strategy: "ordered_fallback", Targets: []RouteTargetInput{{UpstreamModelID: second.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: first.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.Route(ctx, model.ID, RouteOptions{Operation: "chat/completions", EstimatedInputTokens: 10, EstimatedOutputTokens: 10})
	if err != nil || len(plan.Targets) != 2 || plan.Targets[0].UpstreamModelID != second.ID {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO price_versions (id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,source,effective_from,created_at) VALUES ('price_first',?, ?,100,100,'test',?,?),('price_second',?, ?,0,0,'test',?,?)", first.ConnectionID, model.ID, now-1, now, second.ConnectionID, model.ID, now-1, now); err != nil {
		t.Fatal(err)
	}
	model, err = service.ConfigureRoute(ctx, owner, model.ID, model.Revision, RouteConfigInput{Strategy: "lowest_cost", FreeOnly: true, Targets: []RouteTargetInput{{UpstreamModelID: first.ID, Priority: 1, Weight: 1, Enabled: true}, {UpstreamModelID: second.ID, Priority: 2, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = service.Route(ctx, model.ID, RouteOptions{Operation: "chat/completions", EstimatedInputTokens: 10, EstimatedOutputTokens: 10})
	if err != nil || len(plan.Targets) != 1 || plan.Targets[0].UpstreamModelID != second.ID || len(plan.Rejected) != 1 || plan.Rejected[0].Reason != "not_verified_free" {
		t.Fatalf("free plan=%#v err=%v", plan, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE price_versions SET created_at=? WHERE id='price_second'", now-(25*time.Hour).Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Route(ctx, model.ID, RouteOptions{Operation: "chat/completions"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale free price route=%v", err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE price_versions SET created_at=? WHERE id='price_second'", now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET enabled=0 WHERE id=?", second.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if models, err := service.ListPublicModels(ctx); err != nil || len(models) != 2 {
		t.Fatalf("fallback-backed models=%#v err=%v", models, err)
	}
	if _, _, err = service.RouteConfig(ctx, owner, model.ID); err != nil {
		t.Fatalf("route repair unavailable: %v", err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET enabled=0 WHERE id=?", first.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if models, err := service.ListManagedPublicModels(ctx, owner); err != nil || len(models) != 2 {
		t.Fatalf("managed models requiring repair=%#v err=%v", models, err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET enabled=1 WHERE id IN (?,?)", first.ConnectionID, second.ConnectionID); err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0].Target()
	for range 3 {
		if err = service.RecordRouteOutcome(ctx, target, "chat/completions", false, false, 0, time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.Route(ctx, model.ID, RouteOptions{Operation: "chat/completions", EstimatedInputTokens: 10, EstimatedOutputTokens: 10}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("circuit err=%v", err)
	}
}

func TestCatalogValidationAndPresets(t *testing.T) {
	zero := int64(0)
	if _, err := parseCatalog([]byte(`{"version":"2026-09-15","models":[{"provider":"openrouter","model_id":"free/test","label":"Free test","capabilities":["chat"],"input_nanos_per_million":0,"output_nanos_per_million":0,"free":true}]}`)); err != nil {
		t.Fatal(err)
	}
	bad, _ := json.Marshal(catalogDocument{Version: "v1", Models: []CatalogCandidate{{Provider: "openrouter", ModelID: "bad", Label: "Bad", Capabilities: []string{"chat"}, InputNanosPerMillion: &zero, Free: true}}})
	if _, err := parseCatalog(bad); err == nil {
		t.Fatal("free model without explicit output price was accepted")
	}
	if _, err := parseCatalog([]byte(`{"version":"v1","models":[]} {}`)); err == nil {
		t.Fatal("trailing catalog JSON was accepted")
	}
	input, err := validateConnection(ConnectionInput{Name: "Router", Preset: "openrouter", Enabled: true})
	if err != nil || input.Adapter != "openai_compatible" || input.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("preset=%#v err=%v", input, err)
	}
	expected := map[string]string{
		"mistral":    "https://api.mistral.ai/v1",
		"groq":       "https://api.groq.com/openai/v1",
		"deepseek":   "https://api.deepseek.com",
		"xai":        "https://api.x.ai/v1",
		"together":   "https://api.together.ai/v1",
		"fireworks":  "https://api.fireworks.ai/inference/v1",
		"cohere":     "https://api.cohere.ai/compatibility/v1",
		"perplexity": "https://api.perplexity.ai/v1",
	}
	available := Presets()
	for id, baseURL := range expected {
		input, err = validateConnection(ConnectionInput{Name: id, Preset: id, Enabled: true})
		if err != nil || input.Adapter != "openai_compatible" || input.BaseURL != baseURL {
			t.Errorf("%s preset=%#v err=%v", id, input, err)
		}
		found := false
		for _, preset := range available {
			if preset.ID == id {
				found = len(preset.Operations) > 0 && preset.DocumentationURL != "" && preset.ReviewedAt != ""
			}
		}
		if !found {
			t.Errorf("%s preset metadata is incomplete", id)
		}
	}
	available[0].Operations[0] = "mutated"
	if Presets()[0].Operations[0] == "mutated" {
		t.Fatal("preset operations escaped by reference")
	}
	clouds := []ConnectionInput{
		{Name: "Azure", Preset: "azure-openai", BaseURL: "https://gateway.openai.azure.com/openai/v1"},
		{Name: "Bedrock", Preset: "bedrock", BaseURL: "https://bedrock-runtime.eu-central-1.amazonaws.com/openai/v1"},
		{Name: "Vertex", Preset: "vertex", BaseURL: "https://europe-west1-aiplatform.googleapis.com/v1/projects/example/locations/europe-west1/endpoints/openapi"},
	}
	for _, cloud := range clouds {
		validated, err := validateConnection(cloud)
		if err != nil || validated.Adapter != "openai_compatible" || validated.BaseURL != cloud.BaseURL {
			t.Errorf("cloud preset=%#v err=%v", validated, err)
		}
		cloud.BaseURL = "https://example.com/v1"
		if _, err = validateConnection(cloud); err == nil {
			t.Errorf("%s accepted an unrelated endpoint", cloud.Preset)
		}
	}
	for _, invalid := range []ConnectionInput{
		{Name: "Vertex", Preset: "vertex", BaseURL: "https://aiplatform.googleapis.com/projects/example/locations/global/endpoints/openapi"},
		{Name: "Vertex", Preset: "vertex", BaseURL: "https://us-east1-aiplatform.googleapis.com/v1/projects/example/locations/europe-west1/endpoints/openapi"},
		{Name: "Vertex", Preset: "vertex", BaseURL: "https://aiplatform.googleapis.com/v1/projects//locations/global/endpoints/openapi"},
		{Name: "Bedrock", Preset: "bedrock", BaseURL: "https://bedrock-runtime..amazonaws.com/openai/v1"},
	} {
		if _, err = validateConnection(invalid); err == nil {
			t.Errorf("%s accepted malformed cloud endpoint %s", invalid.Preset, invalid.BaseURL)
		}
	}
	if PresetSupports("fireworks", "responses") || PresetSupports("fireworks", "embeddings") || !PresetSupports("fireworks", "chat/completions") || !PresetSupports("gemini", "models/test:generateContent") || !PresetSupports("custom", "anything") {
		t.Fatal("preset operation limits are not enforced")
	}
	if !PresetSupports("openai", "moderations") || PresetSupports("anthropic", "moderations") || !PresetSupportsCapabilities("openai", []string{"moderations"}) {
		t.Fatal("moderation preset capability is incorrect")
	}
}

func TestPresetLimitsUpstreamModelCapabilities(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	connection, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "Fireworks", Preset: "fireworks", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateUpstreamModel(ctx, owner, connection.ID, "model", []string{"embeddings"}); err == nil {
		t.Fatal("chat-only preset accepted embeddings")
	}
	if _, err = service.CreateUpstreamModel(ctx, owner, connection.ID, "model", []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	custom, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "Custom", Adapter: "openai_compatible", BaseURL: "http://127.0.0.1:9000/v1", AllowPrivateNetwork: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateUpstreamModel(ctx, owner, custom.ID, "embedding-model", []string{"embeddings"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.UpdateConnection(ctx, owner, custom.ID, custom.Revision, ConnectionInput{Name: custom.Name, Preset: "fireworks", Enabled: true}); err == nil {
		t.Fatal("preset update stranded an existing embedding model")
	}
}

func TestCatalogSourceChangeClearsCandidates(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	first := "https://raw.githubusercontent.com/example/catalog/main/models.json"
	second := "https://raw.githubusercontent.com/example/catalog/main/next.json"
	if _, err = service.ConfigureCatalog(ctx, owner, first, false, 24); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO catalog_candidates (provider,model_id,label,capabilities_json,free,source,source_version,discovered_at) VALUES ('openrouter','free/test','Test','[\"chat\"]',1,?,'v1',?)", first, now); err != nil {
		t.Fatal(err)
	}
	state, err := service.ConfigureCatalog(ctx, owner, second, false, 24)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM catalog_candidates").Scan(&count); err != nil || count != 0 || state.SourceVersion != "" || state.LastCheckedAt != nil {
		t.Fatalf("count=%d state=%#v err=%v", count, state, err)
	}
}

func TestRouteOrderingIsDeterministic(t *testing.T) {
	cheap, expensive := int64(2), int64(9)
	fast, slow := int64(10), int64(80)
	items := []RouteTarget{{UpstreamModelID: "a", Priority: 1, Weight: 1, estimatedCostNanos: &expensive, LatencyMS: &slow, SampleCount: 10}, {UpstreamModelID: "b", Priority: 2, Weight: 9, estimatedCostNanos: &cheap, LatencyMS: &fast, SampleCount: 10}}
	cost, _, _ := orderRoute(append([]RouteTarget(nil), items...), nil, "lowest_cost", "seed")
	if cost[0].UpstreamModelID != "b" {
		t.Fatalf("cost=%#v", cost)
	}
	latency, _, _ := orderRoute(append([]RouteTarget(nil), items...), nil, "lowest_latency", "ordinary-seed")
	if latency[0].UpstreamModelID != "b" {
		t.Fatalf("latency=%#v", latency)
	}
	first, _, _ := orderRoute(append([]RouteTarget(nil), items...), nil, "weighted", "stable")
	second, _, _ := orderRoute(append([]RouteTarget(nil), items...), nil, "weighted", "stable")
	if first[0].UpstreamModelID != second[0].UpstreamModelID {
		t.Fatal("weighted routing was not deterministic")
	}
	fixed, _, _ := orderRoute(append([]RouteTarget(nil), items...), nil, "fixed", "seed")
	if len(fixed) != 1 || fixed[0].UpstreamModelID != "a" {
		t.Fatalf("fixed=%#v", fixed)
	}
}

func routeFixture(t *testing.T, ctx context.Context, service *Service, owner auth.User, name string) UpstreamModel {
	t.Helper()
	connection, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: name, Adapter: "openai", BaseURL: "http://127.0.0.1:9000/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "secret", ""); err != nil {
		t.Fatal(err)
	}
	model, err := service.CreateUpstreamModel(ctx, owner, connection.ID, name+"-model", []string{"chat", "embeddings"})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
