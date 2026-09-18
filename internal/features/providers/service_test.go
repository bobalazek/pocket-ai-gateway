package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestPromptCacheCapabilityRequiresAnthropicPreset(t *testing.T) {
	if !PresetSupportsCapabilities("anthropic", []string{"chat", "prompt_cache"}) {
		t.Fatal("Anthropic preset rejected prompt_cache")
	}
	for _, preset := range []string{"custom", "openai", "gemini"} {
		if PresetSupportsCapabilities(preset, []string{"prompt_cache"}) {
			t.Fatalf("%s preset accepted prompt_cache", preset)
		}
	}
	if validCapabilities([]string{"prompt_cache"}) {
		t.Fatal("prompt_cache accepted without chat")
	}
}

func TestWebSearchCapabilityRequiresNativePresetAndChat(t *testing.T) {
	for _, preset := range []string{"openai", "anthropic"} {
		if !PresetSupportsCapabilities(preset, []string{"chat", "web_search"}) {
			t.Fatalf("%s preset rejected web_search", preset)
		}
	}
	for _, preset := range []string{"custom", "openai_compatible", "gemini"} {
		if PresetSupportsCapabilities(preset, []string{"chat", "web_search"}) {
			t.Fatalf("%s preset accepted web_search", preset)
		}
	}
	if validCapabilities([]string{"web_search"}) {
		t.Fatal("web_search accepted without chat")
	}
	if !validCapabilities([]string{"chat", "web_search"}) {
		t.Fatal("chat plus web_search was rejected")
	}
	if validCapabilities([]string{"chat", "web_search_dynamic"}) || !validCapabilities([]string{"chat", "web_search", "web_search_dynamic"}) {
		t.Fatal("dynamic web search must include its base capability")
	}
	if !PresetSupportsCapabilities("anthropic", []string{"chat", "web_search", "web_search_dynamic"}) || PresetSupportsCapabilities("openai", []string{"chat", "web_search", "web_search_dynamic"}) {
		t.Fatal("dynamic web search was not restricted to Anthropic")
	}
	for _, provider := range ProviderTypes() {
		hasWebSearch := false
		for _, capability := range provider.Capabilities {
			hasWebSearch = hasWebSearch || capability == "web_search"
		}
		if hasWebSearch != (provider.ID == "openai" || provider.ID == "anthropic") {
			t.Fatalf("provider capability publication = %#v", provider)
		}
	}
}

func TestProviderTypesPublishPresentationMetadata(t *testing.T) {
	defaults := 0
	for _, provider := range ProviderTypes() {
		if provider.Label == "" || len(provider.CapabilityDetails) != len(provider.Capabilities) {
			t.Fatalf("provider label is missing: %#v", provider)
		}
		for _, capability := range provider.CapabilityDetails {
			if capability.ID == "" || capability.Label == "" {
				t.Fatalf("provider capability metadata is incomplete: %#v", provider)
			}
		}
		if provider.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("default providers = %d", defaults)
	}
}

func TestWebFetchCapabilityRequiresAnthropicPresetAndChat(t *testing.T) {
	if !PresetSupportsCapabilities("anthropic", []string{"chat", "web_fetch"}) {
		t.Fatal("Anthropic preset rejected web_fetch")
	}
	for _, preset := range []string{"custom", "openai", "openai_compatible", "gemini"} {
		if PresetSupportsCapabilities(preset, []string{"chat", "web_fetch"}) {
			t.Fatalf("%s preset accepted web_fetch", preset)
		}
	}
	if validCapabilities([]string{"web_fetch"}) {
		t.Fatal("web_fetch accepted without chat")
	}
	if !validCapabilities([]string{"chat", "web_fetch"}) {
		t.Fatal("chat plus web_fetch was rejected")
	}
	if validCapabilities([]string{"chat", "web_fetch_dynamic"}) || !validCapabilities([]string{"chat", "web_fetch", "web_fetch_dynamic"}) {
		t.Fatal("dynamic web fetch must include its base capability")
	}
	if !PresetSupportsCapabilities("anthropic", []string{"chat", "web_fetch", "web_fetch_dynamic"}) || PresetSupportsCapabilities("openai", []string{"chat", "web_fetch", "web_fetch_dynamic"}) {
		t.Fatal("dynamic web fetch was not restricted to Anthropic")
	}
	for _, provider := range ProviderTypes() {
		hasWebFetch := false
		for _, capability := range provider.Capabilities {
			hasWebFetch = hasWebFetch || capability == "web_fetch"
		}
		if hasWebFetch != (provider.ID == "anthropic") {
			t.Fatalf("provider capability publication = %#v", provider)
		}
	}
}

func TestWebToolResponseInclusionAndCacheBypassCapabilities(t *testing.T) {
	if validCapabilities([]string{"chat", "web_search", "web_search_response_inclusion"}) {
		t.Fatal("web_search response inclusion accepted without web_search_dynamic")
	}
	if validCapabilities([]string{"chat", "web_fetch", "web_fetch_cache_bypass", "web_fetch_response_inclusion"}) {
		t.Fatal("web_fetch response inclusion accepted without web_fetch_dynamic")
	}
	if validCapabilities([]string{"chat", "web_fetch", "web_fetch_dynamic", "web_fetch_response_inclusion"}) {
		t.Fatal("web_fetch response inclusion accepted without web_fetch_cache_bypass")
	}
	if !validCapabilities([]string{"chat", "web_search", "web_search_dynamic", "web_search_response_inclusion"}) {
		t.Fatal("complete web_search capability chain was rejected")
	}
	if !validCapabilities([]string{"chat", "web_fetch", "web_fetch_dynamic", "web_fetch_cache_bypass", "web_fetch_response_inclusion"}) {
		t.Fatal("complete web_fetch capability chain was rejected")
	}
	searchChain := []string{"chat", "web_search", "web_search_dynamic", "web_search_response_inclusion"}
	fetchChain := []string{"chat", "web_fetch", "web_fetch_dynamic", "web_fetch_cache_bypass", "web_fetch_response_inclusion"}
	if !PresetSupportsCapabilities("anthropic", searchChain) || !PresetSupportsCapabilities("anthropic", fetchChain) {
		t.Fatal("Anthropic preset rejected a web-tool capability chain")
	}
	for _, preset := range []string{"custom", "openai", "openai_compatible", "gemini"} {
		if PresetSupportsCapabilities(preset, searchChain) || PresetSupportsCapabilities(preset, fetchChain) {
			t.Fatalf("%s preset accepted a web-tool capability chain", preset)
		}
	}
}

func TestMasterKeyAndStoredCredentialRoundTrip(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadOrCreateMasterKey(directory, true); err == nil || !strings.Contains(err.Error(), "stored encrypted data") {
		t.Fatalf("missing required master key error = %v", err)
	}
	first, err := LoadOrCreateMasterKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateMasterKey(directory)
	if err != nil || string(first) != string(second) {
		t.Fatalf("master key did not persist: %v", err)
	}
	info, err := os.Stat(filepath.Join(directory, "master.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode = %v, %v", info.Mode().Perm(), err)
	}
	ciphertext, nonce, err := seal(first, "con_a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openSecret(first, "con_b", ciphertext, nonce); err == nil {
		t.Fatal("credential opened with wrong connection AAD")
	}
}

func TestConnectionModelAndCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	empty, err := service.ListConnections(ctx, owner)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty connections = %#v, %v", empty, err)
	}
	if _, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "blocked", Adapter: "openai", BaseURL: "http://127.0.0.1:9000", Enabled: true}); err == nil {
		t.Fatal("private HTTP connection was accepted")
	}
	connection, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "Local mock", Adapter: "openai", BaseURL: "http://127.0.0.1:9000/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, "gpt-test", []string{"chat", "embeddings", "chat"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstream.ID, []string{"chat", "embeddings"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Target(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Credential != "provider-secret" || target.UpstreamID != "gpt-test" || target.BaseURL != "http://127.0.0.1:9000/v1" {
		t.Fatalf("target = %#v", target)
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "rotated-secret", ""); err != nil {
		t.Fatal(err)
	}
	if service.TargetIsCurrent(ctx, target) {
		t.Fatal("credential rotation left the captured target current")
	}
	target, err = service.Target(ctx, model.ID)
	if err != nil || target.Credential != "rotated-secret" {
		t.Fatalf("rotated target = %#v, %v", target, err)
	}
	connections, err := service.ListConnections(ctx, owner)
	if err != nil || len(connections) != 1 || connections[0].CredentialState != "stored" {
		t.Fatalf("connections = %#v, %v", connections, err)
	}
	if !connections[0].CredentialRequired || !containsString(connections[0].Capabilities, "chat") {
		t.Fatalf("backend connection policy = %#v", connections[0])
	}
	if connections[0].Name == "provider-secret" {
		t.Fatal("credential leaked through connection")
	}
	member := auth.User{ID: "usr_member", Role: "member", Status: "active"}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash,role,status,scopes_json,model_patterns_json,connection_ids_json,created_at,updated_at) VALUES (?,?,?,'hash','member','active','[]','["assistant"]',?, ?, ?)`, member.ID, "member@example.test", "Member", `["`+connection.ID+`"]`, now, now); err != nil {
		t.Fatal(err)
	}
	visible, err := service.ListVisibleModels(ctx, member)
	if err != nil || len(visible) != 1 || visible[0].ID != "assistant" {
		t.Fatalf("visible models = %#v, %v", visible, err)
	}
	var audits int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE actor_user_id=? AND resource_type IN ('provider_connection','upstream_model','public_model')", owner.ID).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("provider audits = %d, %v", audits, err)
	}
	if _, err := service.CreatePublicModel(ctx, owner, "bad/id", "Bad", "", upstream.ID, []string{"chat"}); err == nil {
		t.Fatal("unsafe public model id was accepted")
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "", "env:BAD-NAME"); err == nil {
		t.Fatal("unsafe environment reference was accepted")
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "", "file:relative"); err == nil {
		t.Fatal("relative file reference was accepted")
	}
	tokenFile := filepath.Join(t.TempDir(), "provider-token")
	if err := os.WriteFile(tokenFile, []byte("external-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.PutCredential(ctx, owner, connection.ID, "", "bearer-file:"+tokenFile); err != nil {
		t.Fatal(err)
	}
	target, err = service.Target(ctx, model.ID)
	if err != nil || target.Credential != "external-one" || !target.BearerCredential {
		t.Fatalf("external target = %#v, %v", target, err)
	}
	if err := os.WriteFile(tokenFile, []byte("external-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err = service.Target(ctx, model.ID)
	if err != nil || target.Credential != "external-two" || !target.BearerCredential {
		t.Fatalf("rotated external target = %#v, %v", target, err)
	}
	connection, err = service.getConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateConnection(ctx, owner, connection.ID, connection.Revision, ConnectionInput{Name: connection.Name, Adapter: connection.Adapter, BaseURL: connection.BaseURL, Enabled: false, AllowPrivateNetwork: true, TimeoutMS: connection.TimeoutMS}); err != nil {
		t.Fatal(err)
	}
	if service.TargetIsCurrent(ctx, target) {
		t.Fatal("stale target remained current after connection disable")
	}
}

func TestConnectionPresetChangeClearsCredential(t *testing.T) {
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
	baseURL := "https://resource.openai.azure.com/openai/v1"
	connection, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "Azure", Adapter: "openai_compatible", BaseURL: baseURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "provider-secret", ""); err != nil {
		t.Fatal(err)
	}
	connection, err = service.getConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateConnection(ctx, owner, connection.ID, connection.Revision, ConnectionInput{Name: "Azure", Preset: "azure-openai", BaseURL: baseURL, Enabled: true})
	if err != nil || updated.CredentialState != "missing" {
		t.Fatalf("updated connection = %#v, %v", updated, err)
	}
}
