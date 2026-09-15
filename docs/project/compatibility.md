# Compatibility record

## Deterministic release matrix

| Client surface | Implemented operations | Evidence |
| --- | --- | --- |
| OpenAI Chat Completions | JSON and SSE generation, tools, image input, JSON schema output, embeddings, models | Go fixtures and OpenAI SDK 7.15.0 integration |
| OpenAI Responses | Stored JSON, durable background lifecycle, stateless JSON/SSE, native response compaction, Conversation resources, and atomic synchronous/background/buffered-stream attachment | Go fixtures and OpenAI SDK 7.15.0 integration |
| Anthropic Messages | JSON and native SSE generation, tools, image input, token counting, models | Go fixtures and Anthropic SDK 0.125.0 integration |
| Gemini v1beta | JSON and SSE generation, tools, image input, embeddings, token counting, models | Go fixtures and Google Gen AI 2.22.0 integration |

Cross-provider generation covers the shared documented subset. Anthropic client streaming remains native-target only because input usage is required in the first Anthropic event. Embeddings and token counting require native capable targets. Hosted tools, opaque reasoning blocks, files, batches, realtime, caches, and provider-owned conversations are unsupported until explicitly implemented and tested.

No paid-provider certification is claimed. CI uses deterministic local providers; live provider results must be added here with date, provider API version, model, and exact operations.
