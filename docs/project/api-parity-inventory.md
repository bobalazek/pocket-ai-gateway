# API parity inventory

Updated September 16, 2026. This inventory distinguishes implemented wire compatibility from future API breadth. An endpoint is supported only when its namespace contract and failure behavior are tested. Unknown routes return the selected protocol's safe error rather than being forwarded opportunistically.

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
| `POST /chat/completions` | Constrained | JSON and SSE generation across capable families; `store:true` uses gateway-owned 30-day persistence, disables upstream storage, rejects streaming before dispatch, and accepts string or text/image_url-array message content |
| `POST /completions` | Constrained, native target only | Legacy JSON/SSE on OpenAI or OpenAI-compatible targets with explicit public/upstream `completions`, `completions:generate`, strict official request validation, 16 MiB request-body, JSON-response, and per-SSE-event bounds, candidate-aware output reservation, public-model normalization, and no cross-protocol translation |
| Stored Chat Completion list/retrieve/update/delete/messages | Constrained, gateway-owned | Creating-key ownership, SQL keyset metadata/model filters, bounded cursor bytes, metadata-only updates, original-message pagination, shared retained-result ceilings, and automatic/manual expiry cleanup |
| `POST /responses` | Constrained | Non-streaming responses are gateway-stored for the creating API key by default for 30 days; `background:true` runs through the durable local queue; `store:false` supports JSON and lifecycle SSE; synchronous JSON, background jobs, and bounded buffered streams can atomically attach gateway Conversations |
| Responses `web_search` tool | Constrained, native OpenAI preset only | One `web_search` plus function tools; public/upstream `chat` and `web_search`; `responses:web_search`; explicit 1–4 `max_tool_calls` and positive `max_output_tokens`; buffered JSON for stateless, stored, or background Responses; optional context size, live/cache access, approximate location, allowed/blocked domains, bounded `return_token_budget: "default"`, and `web_search_call.action.sources`; no Conversations/provider references, preview/image search, unlimited search-token return, translation, post-dispatch fallback, free-only/lowest-cost/spend policies, or known search cost |
| `POST /responses/compact` | Native OpenAI preset only | Inline model/input requests are forwarded; response/conversation/item/file/container references and cross-provider approximations are rejected |
| `POST /responses/input_tokens` | Native target only | Counts direct model/input requests; provider-owned response, conversation, item, file, and container references are rejected |
| `POST /embeddings` | Native target only | Preserves order, dimensions, encoding, and usage; no cross-model fallback |
| `POST /moderations` | Native target only | Preserves the upstream OpenAI moderation taxonomy; requires `moderations:classify` and a moderation-capable model |
| `POST /images/generations` | Constrained, native target only | Bounded JSON plus named SSE on built-in OpenAI GPT Image targets; streaming requires `n=1`, accepts 0–3 partial images, limits each event to 16 MiB, and accounts terminal provider usage; explicit public model, `images:generate`, no post-dispatch fallback, and rejection under token/output/spend/free-only policies or lowest-cost routing because the request has no portable output/price contract |
| `POST /images/edits` | Constrained, native target only | Non-streaming GPT Image multipart edit with 1-16 validated PNG/JPEG/WebP inputs, optional same-size PNG mask, 64 MiB aggregate upload and 16 MiB response bounds; explicit public model, prompt, and `images:edit`; DALL-E 2 edits, post-dispatch fallback, token/output/spend/free-only policies, and lowest-cost routing remain unsupported |
| `POST /images/variations` | Constrained, native target only | One square PNG under 4 MB; explicit public model and `images:variation`; OpenAI preset restricted to upstream `dall-e-2`, custom compatible endpoints operator-declared; 64 MiB body and 16 MiB response bounds; no post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/speech` | Constrained, native target only | Built-in voices and buffered audio output up to 16 MiB; explicit public model, `audio:speech`, no custom voice references, SSE, post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/transcriptions` | Constrained, native target only | Multipart upload with one supported audio file up to 25 MB; explicit public model, `audio:transcribe`, provider fields and streaming preserved, no post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/translations` | Constrained, native target only | Multipart audio-to-English translation with one supported file up to 25 MB; explicit public model, `audio:translate`, OpenAI preset restricted to upstream `whisper-1`, custom compatible endpoints operator-declared, 16 MiB response bound, no streaming, post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `GET /responses/{id}`, `DELETE /responses/{id}` | Implemented, gateway-owned | Creating-key retrieval and deletion; the gateway replaces the upstream ID and keeps upstream storage disabled |
| `POST /responses/{id}/cancel`, `GET /responses/{id}/input_items` | Constrained, gateway-owned | Creating-key cancellation and bounded cursor pagination over stored input; `include[]` projections and input from pre-migration Responses return an explicit unsupported-feature error |
| Conversations and conversation items | Constrained, gateway-owned | Resource/item CRUD plus synchronous/background/buffered-stream Response attachment, key isolation, storage bounds, and cursor pagination are implemented; provider references/projections remain pending |
| Files | Constrained, gateway-owned | Five official SDK operations over creating-key-owned local resources; `files:manage`; one non-empty upload for `assistants`, `batch`, `fine-tune`, `vision`, `user_data`, or `evals` up to 16 MiB; JSONL enforced where required; optional 1-hour through 30-day expiry; bounded purpose/order/keyset listing; exact content; delete and expiry become not found |
| Uploads | Constrained, gateway-owned | Four official operations under `files:manage`; one-hour key-owned Uploads assemble a declared 1–16 MiB Batch JSONL File from at most 16 encrypted non-empty Parts in caller-supplied order; exact byte match; completed-File expiry from 1 hour through 30 days; no checksum validation, provider dispatch, or accounting |
| Batches | Constrained, gateway-owned | Four official SDK methods over creating-key-owned resources; `batches:manage` plus the endpoint scope; one same-key Batch File with 1–4 non-streaming `/v1/responses`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/moderations`, `/v1/images/generations`, or `/v1/images/edits` requests for one public model; local routing/accounting; strict native legacy Completion validation; fixed-vector Embeddings; native-only Moderations and Images without fallback; Image Edits accept HTTPS or bounded PNG/JPEG/WebP data URLs and reject provider `file_id`; standard endpoint output; aggregate usage only when terminal Batch usage is complete; 24-hour processing expiry; separate success/error output Files; no provider Batch discount, limits, or rate pool |
| Vector Stores | Constrained, gateway-owned | Five official SDK lifecycle methods for empty key-owned stores; `vector_stores:manage`; bounded metadata/name/description, 1–365-day optional `last_active_at` expiry, shared retained-resource ceilings, order/after/before keyset pagination, cleanup, and uniform not-found boundary; file attachment, file batches, parsed content, semantic search, and Responses `file_search` remain pending |
| Additional OpenAI and provider-owned Batches | Pending | Video endpoints, provider file references, and provider execution need their own validation, capability, billing, and retention contracts |
| DALL-E 2 edits | Deferred | OpenAI marks [DALL-E 2](https://developers.openai.com/api/docs/models/dall-e-2) deprecated; the gateway avoids extra routing complexity for its legacy edit contract |
| Image-edit streaming and video | Pending | Each media format needs its own bounded upload/download, event, and accounting contract |
| Realtime | Pending | Needs WebSocket/WebRTC authentication, event limits, connection accounting, and protocol tests |
| Fine-tuning and evaluations | Pending | Administrative provider resources need ownership, polling, and cost controls |
| Legacy Assistants, Threads, and Runs | Pending | No compatibility alias is exposed |

## Anthropic client namespace

Base URL: `/api/anthropic/v1`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{id}` | Implemented, gateway-owned | Native Anthropic list/detail shape over visible public models |
| `POST /messages` | Implemented | JSON translates across capable families; SSE requires an Anthropic target so input usage is correct in `message_start`, whose model is normalized to the requested public alias |
| `POST /messages/count_tokens` | Native target only | The gateway never estimates a different provider's tokenizer result |
| Message Batches | Constrained, gateway-owned | Six official SDK operations over key-owned local resources; 1–4 ordinary non-streaming Messages per 16 MiB submission; current creating-key grants and shared routing/accounting; 24-hour processing expiry; whole-resource deletion with JSONL results after 29 days; no provider-owned 100,000-item ceiling or native batch discount |
| Files | Pending | Requires encrypted object storage and provider-resource affinity |
| Prompt caching controls | Implemented for native Anthropic Messages | Fixed Anthropic preset plus public/upstream `prompt_cache`; bounded ephemeral controls; JSON/SSE cache-token accounting; no translated or post-dispatch fallback; cache content is never persisted; cost remains unknown until cache rates are versioned |
| Basic hosted web search | Constrained, native Anthropic preset only | One direct `web_search_20250305` plus ordinary application tools; JSON or SSE; required 1–4 `max_uses`; optional mutually exclusive domain controls and approximate location; public/upstream `chat` and `web_search`; `chat:generate` plus `messages:web_search`; native result/citation/continuation/error shapes, public-model normalization in `message_start`, cumulative terminal usage, and reported calls; no prompt-cache combination, dynamic filtering, translation, post-dispatch fallback, free-only/lowest-cost/spend policies, or known search cost |
| Basic hosted web fetch | Constrained, native Anthropic preset only | One direct `web_fetch_20250910`; JSON or SSE; required 1–4 `max_uses` and approximate 1–16,384 text `max_content_tokens` that does not bound binary PDFs; optional citations and mutually exclusive ten-host domain controls; public/upstream `chat` and `web_fetch`; `chat:generate` plus `messages:web_fetch`; native text/PDF result and error blocks, public model, terminal token and fetch usage; provider-reported token pricing; no token/spend policies, free-only/lowest-cost routing, prompt-cache/search combination, Message Batches, later fetch versions, translation, or post-dispatch retry |
| Anthropic dynamic web search/fetch, code execution, computer use, and connectors | Pending | Later provider-hosted tool versions require their own execution, event, capability, and billing contracts |
| Extended thinking and opaque signatures | Pending across translated paths | Requests are rejected when signatures or reasoning semantics cannot be preserved |

## Gemini client namespace

Base URL: `/api/gemini/v1beta`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{model}` | Implemented, gateway-owned | Native Gemini model envelope over visible public models |
| `POST /interactions` | Constrained, native Gemini preset only | Synchronous stateless text input with explicit public/upstream `interactions`, `chat:generate`, forced `store:false`, optional output bound, terminal usage validation, public-model normalization, and no tools, media, state, background, stream, translation, or post-dispatch fallback |
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
