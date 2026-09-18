package providers

import "strings"

type Preset struct {
	ID                 string             `json:"id"`
	Label              string             `json:"label"`
	Adapter            string             `json:"adapter"`
	AdapterLabel       string             `json:"adapter_label"`
	BaseURL            string             `json:"base_url,omitempty"`
	BaseURLExample     string             `json:"base_url_example,omitempty"`
	BaseURLRequired    bool               `json:"base_url_required"`
	CredentialRequired bool               `json:"credential_required"`
	Authentication     string             `json:"authentication,omitempty"`
	PrivateNetwork     bool               `json:"private_network"`
	Operations         []string           `json:"operations"`
	Capabilities       []string           `json:"capabilities"`
	CapabilityDetails  []CapabilityDetail `json:"capability_details"`
	DocumentationURL   string             `json:"documentation_url"`
	ReviewedAt         string             `json:"reviewed_at"`
}

var presets = []Preset{
	{ID: "openai", Label: "OpenAI", Adapter: "openai", BaseURL: "https://api.openai.com/v1", CredentialRequired: true, Operations: []string{"chat/completions", "completions", "responses", "responses/compact", "responses/input_tokens", "embeddings", "moderations", "images/generations", "images/edits", "images/variations", "audio/speech", "audio/transcriptions", "audio/translations", "realtime", "live"}, DocumentationURL: "https://developers.openai.com/api/reference/overview", ReviewedAt: "2026-09-17"},
	{ID: "anthropic", Label: "Anthropic", Adapter: "anthropic", BaseURL: "https://api.anthropic.com/v1", CredentialRequired: true, Operations: []string{"messages", "messages/count_tokens"}, DocumentationURL: "https://platform.claude.com/docs/en/api/overview", ReviewedAt: "2026-09-15"},
	{ID: "gemini", Label: "Google Gemini", Adapter: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", CredentialRequired: true, Operations: []string{"generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents", "interactions", "BidiGenerateContent", "predictLongRunning"}, DocumentationURL: "https://ai.google.dev/api", ReviewedAt: "2026-09-17"},
	{ID: "openrouter", Label: "OpenRouter", Adapter: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "embeddings", "audio/speech"}, DocumentationURL: "https://openrouter.ai/docs/api/reference/overview", ReviewedAt: "2026-09-16"},
	{ID: "zai", Label: "Z.AI", Adapter: "openai_compatible", BaseURL: "https://api.z.ai/api/paas/v4", CredentialRequired: true, Operations: []string{"chat/completions", "images/generations"}, DocumentationURL: "https://docs.z.ai/api-reference/introduction", ReviewedAt: "2026-09-16"},
	{ID: "minimax", Label: "MiniMax", Adapter: "openai_compatible", BaseURL: "https://api.minimax.io/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "responses/input_tokens"}, DocumentationURL: "https://platform.minimax.io/docs/api-reference/text-openai-api", ReviewedAt: "2026-09-16"},
	{ID: "ollama", Label: "Ollama", Adapter: "openai_compatible", BaseURL: "http://127.0.0.1:11434/v1", PrivateNetwork: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.ollama.com/api/openai-compatibility", ReviewedAt: "2026-09-15"},
	{ID: "mistral", Label: "Mistral AI", Adapter: "openai_compatible", BaseURL: "https://api.mistral.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "embeddings", "audio/transcriptions"}, DocumentationURL: "https://docs.mistral.ai/api", ReviewedAt: "2026-09-16"},
	{ID: "groq", Label: "Groq", Adapter: "openai_compatible", BaseURL: "https://api.groq.com/openai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "audio/speech", "audio/transcriptions", "audio/translations"}, DocumentationURL: "https://console.groq.com/docs/openai", ReviewedAt: "2026-09-16"},
	{ID: "deepseek", Label: "DeepSeek", Adapter: "openai_compatible", BaseURL: "https://api.deepseek.com", CredentialRequired: true, Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://api-docs.deepseek.com", ReviewedAt: "2026-09-16"},
	{ID: "xai", Label: "xAI", Adapter: "openai_compatible", BaseURL: "https://api.x.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "embeddings"}, DocumentationURL: "https://docs.x.ai/developers/rest-api-reference/inference", ReviewedAt: "2026-09-16"},
	{ID: "together", Label: "Together AI", Adapter: "openai_compatible", BaseURL: "https://api.together.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "completions", "embeddings", "images/generations", "audio/speech", "audio/transcriptions", "audio/translations", "videos"}, DocumentationURL: "https://docs.together.ai/docs/inference/openai-compatibility", ReviewedAt: "2026-09-17"},
	{ID: "replicate", Label: "Replicate", Adapter: "openai_compatible", BaseURL: "https://api.replicate.com/v1", CredentialRequired: true, Operations: []string{"predictions"}, DocumentationURL: "https://replicate.com/docs/reference/http", ReviewedAt: "2026-09-17"},
	{ID: "fireworks", Label: "Fireworks AI", Adapter: "openai_compatible", BaseURL: "https://api.fireworks.ai/inference/v1", CredentialRequired: true, Operations: []string{"chat/completions", "completions", "responses", "embeddings"}, DocumentationURL: "https://docs.fireworks.ai/tools-sdks/openai-compatibility", ReviewedAt: "2026-09-16"},
	{ID: "cohere", Label: "Cohere", Adapter: "openai_compatible", BaseURL: "https://api.cohere.ai/compatibility/v1", CredentialRequired: true, Operations: []string{"chat/completions", "embeddings"}, DocumentationURL: "https://docs.cohere.com/docs/compatibility-api", ReviewedAt: "2026-09-16"},
	{ID: "perplexity", Label: "Perplexity", Adapter: "openai_compatible", BaseURL: "https://api.perplexity.ai/v1", CredentialRequired: true, Operations: []string{"chat/completions", "responses", "embeddings"}, DocumentationURL: "https://docs.perplexity.ai/docs/agent-api/openai-compatibility", ReviewedAt: "2026-09-16"},
	{ID: "azure-openai", Label: "Azure OpenAI", Adapter: "openai_compatible", BaseURLRequired: true, BaseURLExample: "https://your-resource.openai.azure.com/openai/v1", CredentialRequired: true, Authentication: "API key, or a rotating Microsoft Entra bearer-token reference", Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle", ReviewedAt: "2026-09-16"},
	{ID: "bedrock", Label: "Amazon Bedrock", Adapter: "openai_compatible", BaseURLRequired: true, BaseURLExample: "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1", CredentialRequired: true, Authentication: "Amazon Bedrock bearer API key", Operations: []string{"chat/completions", "responses"}, DocumentationURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/apis.html", ReviewedAt: "2026-09-16"},
	{ID: "vertex", Label: "Google Vertex AI", Adapter: "openai_compatible", BaseURLRequired: true, BaseURLExample: "https://us-central1-aiplatform.googleapis.com/v1/projects/your-project/locations/us-central1/endpoints/openapi", CredentialRequired: true, Authentication: "Rotating Google Cloud bearer-token reference", Operations: []string{"chat/completions"}, DocumentationURL: "https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai", ReviewedAt: "2026-09-16"},
}

func Presets() []Preset {
	items := append([]Preset(nil), presets...)
	for index := range items {
		items[index].AdapterLabel = adapterLabels[items[index].Adapter]
		items[index].Operations = append([]string(nil), items[index].Operations...)
		items[index].Capabilities = availableCapabilities(items[index].ID, items[index].Adapter)
		items[index].CapabilityDetails = capabilityDetails(items[index].Capabilities)
	}
	return items
}

func availableCapabilities(presetID, adapter string) []string {
	capabilities := make([]string, 0, len(adapters[adapter]))
	for _, capability := range adapters[adapter] {
		if PresetSupportsCapabilities(presetID, []string{capability}) {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

func presetCredentialRequired(presetID string) bool {
	for _, preset := range presets {
		if preset.ID == presetID {
			return preset.CredentialRequired
		}
	}
	return true
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

func PresetOperationPath(presetID, operation string) string {
	if presetID == "perplexity" && operation == "chat/completions" {
		return "sonar"
	}
	return operation
}

func PresetSupportsCapabilities(presetID string, capabilities []string) bool {
	for _, capability := range capabilities {
		if capability == "prompt_cache" {
			if presetID == "anthropic" {
				continue
			}
			return false
		}
		if capability == "web_search" {
			if presetID == "openai" || presetID == "anthropic" {
				continue
			}
			return false
		}
		if capability == "web_search_dynamic" || capability == "web_search_response_inclusion" || capability == "web_fetch_dynamic" || capability == "web_fetch_cache_bypass" || capability == "web_fetch_response_inclusion" {
			if presetID == "anthropic" {
				continue
			}
			return false
		}
		if capability == "web_fetch" {
			if presetID == "anthropic" {
				continue
			}
			return false
		}
		if capability == "media_jobs" {
			if presetID == "replicate" || presetID == "together" || presetID == "gemini" || presetID == "custom" {
				continue
			}
			return false
		}
		supported := false
		for _, operation := range []string{"chat/completions", "completions", "messages", "generateContent", "responses", "responses/compact", "responses/input_tokens", "embeddings", "embedContent", "batchEmbedContents", "moderations", "images/generations", "images/edits", "images/variations", "audio/speech", "audio/transcriptions", "audio/translations", "messages/count_tokens", "countTokens", "interactions", "realtime", "live", "BidiGenerateContent", "predictions", "videos", "predictLongRunning"} {
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
	case "completions":
		return "completions"
	case "chat/completions", "messages", "generateContent", "streamGenerateContent", "responses", "responses/compact":
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
	case "interactions":
		return "interactions"
	case "realtime", "live", "BidiGenerateContent":
		return "realtime"
	case "predictions", "videos", "predictLongRunning":
		return "media_jobs"
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
