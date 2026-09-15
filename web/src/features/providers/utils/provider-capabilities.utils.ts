import type { ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";

export const providerCapabilities = ["chat", "embeddings", "moderations", "count_tokens", "images", "image_edit", "image_variation", "audio_speech", "audio_transcription", "audio_translation", "prompt_cache"] as const;
type Capability = (typeof providerCapabilities)[number];

const adapterCapabilities: Record<ProviderConnection["adapter"], Capability[]> = {
  openai: [...providerCapabilities],
  openai_compatible: [...providerCapabilities],
  anthropic: ["chat", "count_tokens", "prompt_cache"],
  gemini: ["chat", "embeddings", "count_tokens"],
};

const capabilityOperations: Record<Capability, string[]> = {
  chat: ["chat/completions", "messages", "generateContent", "responses"],
  embeddings: ["embeddings", "embedContent", "batchEmbedContents"],
  moderations: ["moderations"],
  count_tokens: ["responses/input_tokens", "messages/count_tokens", "countTokens"],
  images: ["images/generations"],
  image_edit: ["images/edits"],
  image_variation: ["images/variations"],
  audio_speech: ["audio/speech"],
  audio_transcription: ["audio/transcriptions"],
  audio_translation: ["audio/translations"],
  prompt_cache: ["messages"],
};

export function availableCapabilities(connection: ProviderConnection, presets: ProviderPreset[]) {
  const supported = adapterCapabilities[connection.adapter];
  if (connection.preset === "custom") return supported.filter((capability) => capability !== "prompt_cache");
  const operations = presets.find((preset) => preset.id === connection.preset)?.operations ?? [];
  return supported.filter((capability) => capability === "prompt_cache" ? connection.preset === "anthropic" : capabilityOperations[capability].some((operation) => operations.includes(operation)));
}
