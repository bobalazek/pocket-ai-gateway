# Provider compatibility and certification

Pocket AI Gateway separates two kinds of evidence:

- **Adapter conformance** uses deterministic local upstreams to verify the gateway's paths, authentication replacement, request and response shapes, errors, usage, streaming, accounting, and limits.
- **Live certification** calls a real provider with a pinned model and credential. It records the provider API revision, operation, result, limitations, date, and test version.

A built-in preset means its endpoint and advertised operations were checked against current official documentation. It does not mean every provider model supports every operation, or that a live paid-provider test ran in public CI.

## Built-in presets

| Provider | Base URL | Adapter | Advertised operations | Documentation reviewed | Live certification |
| --- | --- | --- | --- | --- | --- |
| OpenAI | `https://api.openai.com/v1` | OpenAI | Chat, Responses, input-token counting, embeddings, moderations, image generation, speech | 2026-09-15 | Credentials required |
| Anthropic | `https://api.anthropic.com/v1` | Anthropic | Messages, token counting | 2026-09-15 | Credentials required |
| Google Gemini | `https://generativelanguage.googleapis.com/v1beta` | Gemini | Generation, streaming, token counting, embeddings | 2026-09-15 | Credentials required |
| OpenRouter | `https://openrouter.ai/api/v1` | OpenAI compatible | Chat | 2026-09-15 | Credentials required |
| Ollama | `http://127.0.0.1:11434/v1` | OpenAI compatible | Chat, embeddings | 2026-09-15 | Local model required |
| Mistral AI | `https://api.mistral.ai/v1` | OpenAI compatible | Chat, embeddings | 2026-09-15 | Credentials required |
| Groq | `https://api.groq.com/openai/v1` | OpenAI compatible | Chat, Responses | 2026-09-15 | Credentials required |
| DeepSeek | `https://api.deepseek.com` | OpenAI compatible | Chat, Responses | 2026-09-15 | Credentials required |
| xAI | `https://api.x.ai/v1` | OpenAI compatible | Chat, Responses, embeddings | 2026-09-15 | Credentials required |
| Together AI | `https://api.together.ai/v1` | OpenAI compatible | Chat, embeddings | 2026-09-15 | Credentials required |
| Fireworks AI | `https://api.fireworks.ai/inference/v1` | OpenAI compatible | Chat | 2026-09-15 | Credentials required |
| Cohere | `https://api.cohere.ai/compatibility/v1` | OpenAI compatible | Chat, embeddings | 2026-09-15 | Credentials required |
| Perplexity | `https://api.perplexity.ai/v1` | OpenAI compatible | Chat, Responses | 2026-09-15 | Credentials required |
| Azure OpenAI | Installation resource URL ending in `/openai/v1` | OpenAI compatible | Chat, Responses | 2026-09-15 | Credentials required |
| Amazon Bedrock | Regional runtime `/openai/v1` or Mantle `/v1` URL | OpenAI compatible | Chat, Responses | 2026-09-15 | Credentials required |
| Google Vertex AI | Project/location URL ending in `/endpoints/openapi` | OpenAI compatible | Chat | 2026-09-15 | Credentials required |

Official references: [OpenAI](https://developers.openai.com/api/reference/overview), [Anthropic](https://platform.claude.com/docs/en/api/overview), [Gemini](https://ai.google.dev/api), [OpenRouter](https://openrouter.ai/docs/api/reference/overview), [Ollama](https://docs.ollama.com/api/openai-compatibility), [Mistral](https://docs.mistral.ai/api), [Groq](https://console.groq.com/docs/openai), [DeepSeek](https://api-docs.deepseek.com), [xAI](https://docs.x.ai/developers/rest-api-reference/inference), [Together](https://docs.together.ai/docs/inference/openai-compatibility), [Fireworks](https://docs.fireworks.ai/tools-sdks/openai-compatibility), [Cohere](https://docs.cohere.com/docs/compatibility-api), [Perplexity](https://docs.perplexity.ai/docs/agent-api/openai-compatibility), [Azure OpenAI](https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle), [Amazon Bedrock](https://docs.aws.amazon.com/bedrock/latest/userguide/apis.html), and [Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai).

## Deterministic evidence

Ordinary verification covers:

- immutable preset-to-adapter and preset-to-base-URL mappings;
- bearer, `x-api-key`, and `x-goog-api-key` credential replacement;
- OpenAI Chat, stateless/stored/background Responses including bounded native web search, input-token counting, embeddings, moderations, non-streaming image generation, buffered speech, and streaming against local upstreams;
- native Anthropic Messages/token counting and Gemini generation/counting/embeddings;
- cross-protocol request, response, tool, refusal, usage, and stream mappings;
- key grants, limits, request accounting, fallback attempts, cancellation, response bounds, and safe provider errors.

Run the complete evidence set with `./scripts/verify.sh`.

## Adding a live result

Record live evidence only when the exact provider call ran. Add one row with: date, provider API version, pinned model, operation, stream mode, gateway revision, official SDK and version when used, result, cost ceiling, and any unsupported fields. Never expose credentials or response content in the record.

Azure OpenAI sends a configured API key in `api-key`. Amazon Bedrock uses a bearer Bedrock API key; SigV4 is not implemented. Vertex AI uses a bearer Google Cloud access token; the operator must refresh the token before it expires. These scoped presets validate their documented public endpoint shapes so a provider credential cannot be paired accidentally with an unrelated host.
