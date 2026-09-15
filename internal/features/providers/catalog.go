package providers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

const maxCatalogBytes = 1 << 20

type Preset struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	Adapter            string   `json:"adapter"`
	BaseURL            string   `json:"base_url,omitempty"`
	BaseURLRequired    bool     `json:"base_url_required"`
	CredentialRequired bool     `json:"credential_required"`
	PrivateNetwork     bool     `json:"private_network"`
	Operations         []string `json:"operations"`
	DocumentationURL   string   `json:"documentation_url"`
	ReviewedAt         string   `json:"reviewed_at"`
}

var presets = []Preset{
	{ID: "openai", Label: "OpenAI", Adapter: "openai", BaseURL: "https://api.openai.com/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "responses/compact", "responses/input_tokens", "embeddings", "moderations", "images/generations", "images/edits", "images/variations", "audio/speech", "audio/transcriptions", "audio/translations"}, DocumentationURL: "https://developers.openai.com/api/reference/overview", ReviewedAt: "2026-09-15"},
	{ID: "anthropic", Label: "Anthropic", Adapter: "anthropic", BaseURL: "https://api.anthropic.com/v1", CredentialRequired: true, Operations: []string{"messages", "messages/count_tokens"}, DocumentationURL: "https://platform.claude.com/docs/en/api/overview", ReviewedAt: "2026-09-15"},
	{ID: "gemini", Label: "Google Gemini", Adapter: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", CredentialRequired: true, Operations: []string{"generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents"}, DocumentationURL: "https://ai.google.dev/api", ReviewedAt: "2026-09-15"},
	{ID: "openrouter", Label: "OpenRouter", Adapter: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1", CredentialRequired: true, Operations: []string{"chat/completions"}, DocumentationURL: "https://openrouter.ai/docs/api/reference/overview", ReviewedAt: "2026-09-15"},
	{ID: "ollama", Label: "Ollama", Adapter: "openai_compatible", BaseURL: "http://127.0.0.1:11434/v1", PrivateNetwork: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.ollama.com/api/openai-compatibility", ReviewedAt: "2026-09-15"},
	{ID: "mistral", Label: "Mistral AI", Adapter: "openai_compatible", BaseURL: "https://api.mistral.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.mistral.ai/api", ReviewedAt: "2026-09-15"},
	{ID: "groq", Label: "Groq", Adapter: "openai_compatible", BaseURL: "https://api.groq.com/openai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://console.groq.com/docs/openai", ReviewedAt: "2026-09-15"},
	{ID: "deepseek", Label: "DeepSeek", Adapter: "openai_compatible", BaseURL: "https://api.deepseek.com", CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://api-docs.deepseek.com", ReviewedAt: "2026-09-15"},
	{ID: "xai", Label: "xAI", Adapter: "openai_compatible", BaseURL: "https://api.x.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "embeddings"}, DocumentationURL: "https://docs.x.ai/developers/rest-api-reference/inference", ReviewedAt: "2026-09-15"},
	{ID: "together", Label: "Together AI", Adapter: "openai_compatible", BaseURL: "https://api.together.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.together.ai/docs/inference/openai-compatibility", ReviewedAt: "2026-09-15"},
	{ID: "fireworks", Label: "Fireworks AI", Adapter: "openai_compatible", BaseURL: "https://api.fireworks.ai/inference/v1", CredentialRequired: true, Operations: []string{"chat/completions"}, DocumentationURL: "https://docs.fireworks.ai/tools-sdks/openai-compatibility", ReviewedAt: "2026-09-15"},
	{ID: "cohere", Label: "Cohere", Adapter: "openai_compatible", BaseURL: "https://api.cohere.ai/compatibility/v1", CredentialRequired: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.cohere.com/docs/compatibility-api", ReviewedAt: "2026-09-15"},
	{ID: "perplexity", Label: "Perplexity", Adapter: "openai_compatible", BaseURL: "https://api.perplexity.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://docs.perplexity.ai/docs/agent-api/openai-compatibility", ReviewedAt: "2026-09-15"},
	{ID: "azure-openai", Label: "Azure OpenAI", Adapter: "openai_compatible", BaseURLRequired: true, CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle", ReviewedAt: "2026-09-15"},
	{ID: "bedrock", Label: "Amazon Bedrock", Adapter: "openai_compatible", BaseURLRequired: true, CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/apis.html", ReviewedAt: "2026-09-15"},
	{ID: "vertex", Label: "Google Vertex AI", Adapter: "openai_compatible", BaseURLRequired: true, CredentialRequired: true, Operations: []string{"chat/completions"}, DocumentationURL: "https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai", ReviewedAt: "2026-09-15"},
}

type CatalogCandidate struct {
	Provider              string   `json:"provider"`
	ModelID               string   `json:"model_id"`
	Label                 string   `json:"label"`
	Capabilities          []string `json:"capabilities"`
	InputNanosPerMillion  *int64   `json:"input_nanos_per_million,omitempty"`
	OutputNanosPerMillion *int64   `json:"output_nanos_per_million,omitempty"`
	Free                  bool     `json:"free"`
	Source                string   `json:"source"`
	SourceVersion         string   `json:"source_version"`
	DiscoveredAt          string   `json:"discovered_at"`
}

type CatalogState struct {
	SourceURL            string  `json:"source_url"`
	SourceVersion        string  `json:"source_version"`
	LastCheckedAt        *string `json:"last_checked_at"`
	LastError            string  `json:"last_error"`
	RefreshEnabled       bool    `json:"refresh_enabled"`
	RefreshIntervalHours int64   `json:"refresh_interval_hours"`
}

type catalogDocument struct {
	Version string             `json:"version"`
	Models  []CatalogCandidate `json:"models"`
}

func Presets() []Preset {
	items := append([]Preset(nil), presets...)
	for index := range items {
		items[index].Operations = append([]string(nil), items[index].Operations...)
	}
	return items
}

func PresetSupports(presetID, operation string) bool {
	if presetID == "" || presetID == "custom" {
		return true
	}
	operation = strings.TrimLeft(strings.SplitN(operation, "?", 2)[0], "/")
	if separator := strings.LastIndex(operation, ":"); separator >= 0 {
		operation = operation[separator+1:]
	}
	for _, preset := range presets {
		if preset.ID == presetID {
			for _, supported := range preset.Operations {
				if supported == operation {
					return true
				}
			}
			return false
		}
	}
	return false
}

func PresetSupportsCapabilities(presetID string, capabilities []string) bool {
	for _, capability := range capabilities {
		supported := false
		for _, operation := range []string{"chat/completions", "messages", "generateContent", "responses", "responses/compact", "responses/input_tokens", "embeddings", "embedContent", "batchEmbedContents", "moderations", "images/generations", "images/edits", "images/variations", "audio/speech", "audio/transcriptions", "audio/translations", "messages/count_tokens", "countTokens"} {
			if operationCapability(operation) == capability && PresetSupports(presetID, operation) {
				supported = true
				break
			}
		}
		if !supported {
			return false
		}
	}
	return true
}

func PresetSupportsModelCapabilities(presetID, upstreamID string, capabilities []string) bool {
	if !PresetSupportsCapabilities(presetID, capabilities) {
		return false
	}
	if presetID == "openai" && upstreamID != "whisper-1" {
		for _, capability := range capabilities {
			if capability == "audio_translation" {
				return false
			}
		}
	}
	if presetID == "openai" && upstreamID != "dall-e-2" {
		for _, capability := range capabilities {
			if capability == "image_variation" {
				return false
			}
		}
	}
	if presetID == "openai" {
		for _, capability := range capabilities {
			if capability == "image_edit" && upstreamID == "dall-e-2" {
				return false
			}
		}
	}
	return true
}

func operationCapability(operation string) string {
	switch operation {
	case "chat/completions", "messages", "generateContent", "responses", "responses/compact":
		return "chat"
	case "embeddings", "embedContent", "batchEmbedContents":
		return "embeddings"
	case "moderations":
		return "moderations"
	case "images/generations":
		return "images"
	case "images/edits":
		return "image_edit"
	case "images/variations":
		return "image_variation"
	case "audio/speech":
		return "audio_speech"
	case "audio/transcriptions":
		return "audio_transcription"
	case "audio/translations":
		return "audio_translation"
	case "responses/input_tokens", "messages/count_tokens", "countTokens":
		return "count_tokens"
	default:
		return ""
	}
}

func presetAllowed(id, adapter string) bool {
	if id == "custom" {
		return true
	}
	for _, preset := range presets {
		if preset.ID == id && preset.Adapter == adapter {
			return true
		}
	}
	return false
}

func applyPreset(input ConnectionInput) ConnectionInput {
	for _, preset := range presets {
		if preset.ID == input.Preset {
			input.Adapter = preset.Adapter
			if preset.BaseURL != "" {
				input.BaseURL = preset.BaseURL
			}
			input.AllowPrivateNetwork = preset.PrivateNetwork
			return input
		}
	}
	return input
}

func (service *Service) Catalog(ctx context.Context, actor auth.User) ([]CatalogCandidate, CatalogState, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return nil, CatalogState{}, err
	}
	rows, err := service.database.QueryContext(ctx, "SELECT provider,model_id,label,capabilities_json,input_nanos_per_million,output_nanos_per_million,free,source,source_version,discovered_at FROM catalog_candidates ORDER BY provider,model_id")
	if err != nil {
		return nil, CatalogState{}, err
	}
	defer rows.Close()
	items := make([]CatalogCandidate, 0)
	for rows.Next() {
		var item CatalogCandidate
		var raw string
		var input, output sql.NullInt64
		var discovered int64
		if err := rows.Scan(&item.Provider, &item.ModelID, &item.Label, &raw, &input, &output, &item.Free, &item.Source, &item.SourceVersion, &discovered); err != nil {
			return nil, CatalogState{}, err
		}
		_ = json.Unmarshal([]byte(raw), &item.Capabilities)
		if input.Valid {
			item.InputNanosPerMillion = &input.Int64
		}
		if output.Valid {
			item.OutputNanosPerMillion = &output.Int64
		}
		item.DiscoveredAt = time.UnixMilli(discovered).UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, CatalogState{}, err
	}
	state, err := service.catalogState(ctx)
	return items, state, err
}

func (service *Service) ConfigureCatalog(ctx context.Context, actor auth.User, sourceURL string, enabled bool, interval int64) (CatalogState, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return CatalogState{}, err
	}
	if sourceURL != "" {
		if _, err := catalogURL(sourceURL); err != nil {
			return CatalogState{}, err
		}
	}
	if interval < 1 || interval > 720 {
		return CatalogState{}, errors.New("refresh_interval_hours must be between 1 and 720")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return CatalogState{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return CatalogState{}, err
	}
	var currentSource string
	if err = tx.QueryRowContext(ctx, "SELECT source_url FROM catalog_refresh_state WHERE singleton=1").Scan(&currentSource); err != nil {
		return CatalogState{}, err
	}
	if currentSource != sourceURL {
		if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_candidates"); err != nil {
			return CatalogState{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET source_url=?,source_version='',last_checked_at=NULL,last_error='',refresh_enabled=?,refresh_interval_hours=? WHERE singleton=1", sourceURL, enabled, interval)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET refresh_enabled=?,refresh_interval_hours=? WHERE singleton=1", enabled, interval)
	}
	if err != nil {
		return CatalogState{}, err
	}
	if err = audit(ctx, tx, actor.ID, "catalog.configure", "catalog", "source"); err != nil {
		return CatalogState{}, err
	}
	if err = tx.Commit(); err != nil {
		return CatalogState{}, err
	}
	return service.catalogState(ctx)
}

func (service *Service) RefreshCatalog(ctx context.Context, actor auth.User) (CatalogState, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return CatalogState{}, err
	}
	state, err := service.catalogState(ctx)
	if err != nil {
		return CatalogState{}, err
	}
	if state.SourceURL == "" {
		return state, errors.New("catalog source_url is required")
	}
	return service.refreshCatalog(ctx, state, &actor)
}

func (service *Service) RunCatalogRefresh(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		state, err := service.catalogState(ctx)
		if err == nil && state.RefreshEnabled && state.SourceURL != "" && catalogRefreshDue(state) {
			_, _ = service.refreshCatalog(ctx, state, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func catalogRefreshDue(state CatalogState) bool {
	if state.LastCheckedAt == nil {
		return true
	}
	checked, err := time.Parse(time.RFC3339Nano, *state.LastCheckedAt)
	return err != nil || time.Since(checked) >= time.Duration(state.RefreshIntervalHours)*time.Hour
}

func (service *Service) refreshCatalog(ctx context.Context, state CatalogState, actor *auth.User) (CatalogState, error) {
	document, fetchErr := fetchCatalog(ctx, state.SourceURL)
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if actor != nil {
		if err = requireManager(ctx, tx, actor); err != nil {
			return state, err
		}
	}
	var currentSource string
	if err = tx.QueryRowContext(ctx, "SELECT source_url FROM catalog_refresh_state WHERE singleton=1").Scan(&currentSource); err != nil {
		return state, err
	}
	if currentSource != state.SourceURL {
		return state, ErrConflict
	}
	actorID := ""
	if actor != nil {
		actorID = actor.ID
	}
	if fetchErr != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET last_checked_at=?,last_error=? WHERE singleton=1", now, truncate(fetchErr.Error(), 500)); err != nil {
			return state, err
		}
		if err = audit(ctx, tx, actorID, "catalog.refresh.failed", "catalog", "source"); err != nil {
			return state, err
		}
		if err = tx.Commit(); err != nil {
			return state, err
		}
		updated, stateErr := service.catalogState(ctx)
		if stateErr != nil {
			return updated, stateErr
		}
		return updated, fetchErr
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_candidates"); err != nil {
		return state, err
	}
	for _, item := range document.Models {
		raw, _ := json.Marshal(item.Capabilities)
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_candidates (provider,model_id,label,capabilities_json,input_nanos_per_million,output_nanos_per_million,free,source,source_version,discovered_at) VALUES (?,?,?,?,?,?,?,?,?,?)", item.Provider, item.ModelID, item.Label, string(raw), item.InputNanosPerMillion, item.OutputNanosPerMillion, item.Free, state.SourceURL, document.Version, now); err != nil {
			return state, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET source_version=?,last_checked_at=?,last_error='' WHERE singleton=1", document.Version, now); err != nil {
		return state, err
	}
	if err = audit(ctx, tx, actorID, "catalog.refresh", "catalog", document.Version); err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	return service.catalogState(ctx)
}

func (service *Service) catalogState(ctx context.Context) (CatalogState, error) {
	var item CatalogState
	var checked sql.NullInt64
	if err := service.database.QueryRowContext(ctx, "SELECT source_url,source_version,last_checked_at,last_error,refresh_enabled,refresh_interval_hours FROM catalog_refresh_state WHERE singleton=1").Scan(&item.SourceURL, &item.SourceVersion, &checked, &item.LastError, &item.RefreshEnabled, &item.RefreshIntervalHours); err != nil {
		return item, err
	}
	if checked.Valid {
		value := time.UnixMilli(checked.Int64).UTC().Format(time.RFC3339Nano)
		item.LastCheckedAt = &value
	}
	return item, nil
}

func fetchCatalog(ctx context.Context, source string) (catalogDocument, error) {
	parsed, err := catalogURL(source)
	if err != nil {
		return catalogDocument{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return catalogDocument{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		_, err := catalogURL(next.URL.String())
		return err
	}}
	response, err := client.Do(request)
	if err != nil {
		return catalogDocument{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return catalogDocument{}, errors.New("catalog source returned " + response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil || len(raw) > maxCatalogBytes {
		return catalogDocument{}, errors.New("catalog response exceeds 1 MiB")
	}
	return parseCatalog(raw)
}

func parseCatalog(raw []byte) (catalogDocument, error) {
	var doc catalogDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return doc, errors.New("invalid catalog document")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return doc, errors.New("invalid catalog document")
	}
	if doc.Version == "" || len(doc.Version) > 100 || len(doc.Models) > 5000 {
		return doc, errors.New("catalog version and at most 5000 models are required")
	}
	seen := map[string]bool{}
	for i := range doc.Models {
		item := &doc.Models[i]
		item.Provider = strings.TrimSpace(item.Provider)
		item.ModelID = strings.TrimSpace(item.ModelID)
		item.Label = strings.TrimSpace(item.Label)
		item.Capabilities = normalizeCapabilities(item.Capabilities)
		key := item.Provider + "\x00" + item.ModelID
		if item.Provider == "" || item.ModelID == "" || item.Label == "" || len(item.Provider) > 100 || len(item.ModelID) > 300 || len(item.Label) > 300 || len(item.Capabilities) == 0 || !validCapabilities(item.Capabilities) || seen[key] || (item.Free && (item.InputNanosPerMillion == nil || item.OutputNanosPerMillion == nil || *item.InputNanosPerMillion != 0 || *item.OutputNanosPerMillion != 0)) {
			return doc, errors.New("catalog model metadata is invalid")
		}
		seen[key] = true
	}
	return doc, nil
}

func catalogURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Hostname() != "raw.githubusercontent.com" && parsed.Hostname() != "github.com") {
		return nil, errors.New("catalog source must be an HTTPS GitHub URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func ValidateCatalogURL(value string) error {
	if value == "" {
		return nil
	}
	_, err := catalogURL(value)
	return err
}
func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
