# API parity inventory

Updated September 15, 2026. This inventory distinguishes implemented wire compatibility from future API breadth. An endpoint is supported only when its namespace contract and failure behavior are tested. Unknown routes return the selected protocol's safe error rather than being forwarded opportunistically.

## Status terms

| Status | Meaning |
| --- | --- |
| Implemented | Served by Pocket AI Gateway and covered by deterministic tests |
| Constrained | Served with a documented restriction that is validated before dispatch |
| Native target only | Requires an upstream that implements the operation natively |
| Pending | Not exposed by the gateway |
| Gateway-owned | Returned from local configuration rather than an upstream resource |

## OpenAI client namespace

Base URL: `/api/openai/v1`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{id}` | Implemented, gateway-owned | Returns only public models visible to the key owner |
| `POST /chat/completions` | Implemented | JSON and SSE; shared text, image, structured-output, tool, refusal, stop, and usage semantics translate across capable families |
| `POST /responses` | Constrained | Non-streaming responses are gateway-stored for the creating API key by default for 30 days; `background:true` runs through the durable local queue; `store:false` supports JSON and lifecycle SSE; synchronous JSON can atomically attach gateway Conversations; hosted tools are rejected |
| `POST /responses/compact` | Native OpenAI preset only | Inline model/input requests are forwarded; response/conversation/item/file/container references and cross-provider approximations are rejected |
| `POST /embeddings` | Native target only | Preserves order, dimensions, encoding, and usage; no cross-model fallback |
| `GET /responses/{id}`, `DELETE /responses/{id}` | Implemented, gateway-owned | Creating-key retrieval and deletion; the gateway replaces the upstream ID and keeps upstream storage disabled |
| `POST /responses/{id}/cancel`, `GET /responses/{id}/input_items` | Constrained, gateway-owned | Creating-key cancellation and bounded cursor pagination over stored input; `include[]` projections and input from pre-migration Responses return an explicit unsupported-feature error |
| Conversations and conversation items | Constrained, gateway-owned | Resource/item CRUD plus synchronous JSON Response attachment, key isolation, storage bounds, and cursor pagination are implemented; streaming/background attachment and provider references/projections remain pending |
| Files, uploads, and vector stores | Pending | Needs encrypted object storage, quotas, scanning, and lifecycle rules |
| Batches | Pending | Needs durable jobs, cancellation, billing, result retention, and restart recovery |
| Images, audio, video, and moderations | Pending | Each media format needs its own bounded upload/download and accounting contract |
| Realtime | Pending | Needs WebSocket/WebRTC authentication, event limits, connection accounting, and protocol tests |
| Fine-tuning and evaluations | Pending | Administrative provider resources need ownership, polling, and cost controls |
| Legacy Assistants, Threads, and Runs | Pending | No compatibility alias is exposed |

## Anthropic client namespace

Base URL: `/api/anthropic/v1`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{id}` | Implemented, gateway-owned | Native Anthropic list/detail shape over visible public models |
| `POST /messages` | Implemented | JSON translates across capable families; SSE currently requires an Anthropic target so input usage is correct in `message_start` |
| `POST /messages/count_tokens` | Native target only | The gateway never estimates a different provider's tokenizer result |
| Message Batches | Pending | Requires durable jobs, result ownership, cancellation, retention, and charging |
| Files | Pending | Requires encrypted object storage and provider-resource affinity |
| Prompt caching controls | Pending across translated paths | Native opaque cache directives are not silently discarded |
| Hosted web search, code execution, computer use, and connectors | Pending | Provider-hosted tools require explicit capability and billing contracts |
| Extended thinking and opaque signatures | Pending across translated paths | Requests are rejected when signatures or reasoning semantics cannot be preserved |

## Gemini client namespace

Base URL: `/api/gemini/v1beta`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{model}` | Implemented, gateway-owned | Native Gemini model envelope over visible public models |
| `POST /models/{model}:generateContent` | Implemented | JSON shared-subset translation across capable families |
| `POST /models/{model}:streamGenerateContent?alt=sse` | Implemented | Native Gemini response envelopes; bounded incremental parser |
| `POST /models/{model}:countTokens` | Native target only | No invented tokenizer result |
| `POST /models/{model}:embedContent` | Native target only | One public embedding model maps to one vector space |
| `POST /models/{model}:batchEmbedContents` | Native target only | Bounded batch with stable ordering |
| Cached content | Pending | Needs provider affinity, expiry, ownership, and cost accounting |
| Files and resumable media upload | Pending | Needs upload limits, storage ownership, scanning, and cleanup |
| Batch generation | Pending | Needs the shared durable-job contract |
| Tuning, permissions, corpora, and generated media | Pending | Administrative/resource APIs need separate authorization and lifecycle design |
| Live API | Pending | Needs a dedicated bidirectional session and event contract |
| Thought signatures | Pending across translated paths | No opaque signature is stripped or fabricated |

## Implemented shared field families

The compatibility suite covers the common field families below on each eligible native or translated route:

- model selection, system instructions, multi-turn messages, text parts, output bounds, stop sequences, and sampling controls;
- image input on capable targets and JSON schema structured output;
- function declarations, tool choice, multiple calls, call IDs, results, and fragmented streamed arguments;
- finish reasons, refusals, safety metadata where representable, token usage, unknown usage, and request IDs;
- streaming cancellation, malformed or truncated upstream output, response bounds, provider errors, and timeouts.

Provider-specific fields outside the shared representation must have an explicit native pass-through or translation rule. The gateway rejects unrecognized semantics that could change the meaning, security, cost, or persistence of a request.

Primary references: [OpenAI API reference](https://developers.openai.com/api/reference/overview), [Anthropic API overview](https://platform.claude.com/docs/en/api/overview), and [Gemini API reference](https://ai.google.dev/api).
