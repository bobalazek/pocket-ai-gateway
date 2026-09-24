# API parity inventory

Updated September 24, 2026. This inventory distinguishes implemented wire compatibility from future API breadth. An endpoint is supported only when its namespace contract and failure behavior are tested. Unknown routes return the selected protocol's safe error rather than being forwarded opportunistically.

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
| Responses `web_search` tool | Constrained, native OpenAI preset only | One `web_search` plus function tools; public/upstream `chat` and `web_search`; `responses:web_search`; explicit 1–4 `max_tool_calls` and positive `max_output_tokens`; buffered JSON for stateless, stored, or background Responses; optional context size, live/cache access, approximate location, allowed/blocked domains, bounded `return_token_budget: "default"`, and `web_search_call.action.sources`; no Conversations/provider references, preview/image search, unlimited search-token return, translation, post-dispatch fallback, or free-only/lowest-cost routing; strict spend needs a configured per-call fee and cache-read rate, and actual cost uses completed calls plus tokens |
| Responses `file_search` tool | Constrained, gateway-owned | One `file_search` plus function tools; 1–10 same-key local Vector Stores; `responses:file_search` plus `responses:generate`; native OpenAI/OpenAI-compatible Responses target; 1–4 model-selected query calls; lexical filters/score threshold and optional results; aggregate provider usage; stateless, stored, and background JSON; no streaming, Conversations, previous-response references, web-search combination, semantic/hybrid ranking, prompt templates/cache controls, translation, synthesized file citations, or post-dispatch fallback |
| `POST /responses/compact` | Native OpenAI preset only | Inline model/input requests are forwarded; response/conversation/item/file/container references and cross-provider approximations are rejected |
| `POST /responses/input_tokens` | Native target only | Counts direct model/input requests; provider-owned response, conversation, item, file, and container references are rejected |
| `POST /embeddings` | Native target only | Preserves order, dimensions, encoding, and usage; no cross-model fallback |
| `POST /moderations` | Native target only | Preserves the upstream OpenAI moderation taxonomy; requires `moderations:classify` and a moderation-capable model |
| `POST /images/generations` | Constrained, native target only | Bounded JSON plus named SSE on built-in OpenAI GPT Image targets; streaming requires `n=1`, accepts 0–3 partial images, limits each event to 16 MiB, and accounts terminal provider usage; explicit public model, `images:generate`, no post-dispatch fallback, and rejection under token/output/spend/free-only policies or lowest-cost routing because the request has no portable output/price contract |
| `POST /images/edits` | Constrained, native target only | GPT Image multipart edit with 1-16 validated PNG/JPEG/WebP inputs, optional same-size PNG mask, 64 MiB aggregate upload and 16 MiB JSON/event bounds; named SSE requires `n=1`, accepts 0–3 partial images, and accounts terminal usage; explicit public model, prompt, and `images:edit`; DALL-E 2 edits, post-dispatch fallback, token/output/spend/free-only policies, and lowest-cost routing remain unsupported |
| `POST /images/variations` | Constrained, native target only | One square PNG under 4 MB; explicit public model and `images:variation`; OpenAI preset restricted to upstream `dall-e-2`, custom compatible endpoints operator-declared; 64 MiB body and 16 MiB response bounds; no post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/speech` | Constrained, native target only | Buffered output up to 16 MiB or explicit HTTP streaming up to 64 MiB; built-in, custom-reference, and provider voice names; raw audio or SSE passthrough; explicit public model and `audio:speech`; no post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/transcriptions` | Constrained, native target only | Multipart upload with one supported audio file up to 25 MB; explicit public model, `audio:transcribe`, provider fields preserved, streaming limited to presets with the OpenAI terminal event contract, no post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `POST /audio/translations` | Constrained, native target only | Multipart audio-to-English translation with one supported file up to 25 MB; explicit public model, `audio:translate`, OpenAI preset restricted to upstream `whisper-1`, custom compatible endpoints operator-declared, 16 MiB response bound, no streaming, post-dispatch fallback, token/output/spend/free-only policies, or lowest-cost routing |
| `GET /responses/{id}`, `DELETE /responses/{id}` | Implemented, gateway-owned | Creating-key retrieval and deletion; the gateway replaces the upstream ID and keeps upstream storage disabled |
| `POST /responses/{id}/cancel`, `GET /responses/{id}/input_items` | Constrained, gateway-owned | Creating-key cancellation and bounded cursor pagination over stored input; `include[]` projections and input from pre-migration Responses return an explicit unsupported-feature error |
| Conversations and conversation items | Constrained, gateway-owned | Resource/item CRUD plus synchronous/background/buffered-stream Response attachment, key isolation, storage bounds, and cursor pagination are implemented; provider references/projections remain pending |
| Files | Constrained, gateway-owned | Five official SDK operations over creating-key-owned local resources; `files:manage`; one non-empty upload for `assistants`, `batch`, `fine-tune`, `vision`, `user_data`, or `evals` up to 16 MiB; JSONL enforced where required; optional 1-hour through 30-day expiry; bounded purpose/order/keyset listing; exact content; delete and expiry become not found |
| Uploads | Constrained, gateway-owned | Four official operations under `files:manage`; one-hour key-owned Uploads assemble a declared 1–16 MiB Batch JSONL File from at most 16 encrypted non-empty Parts in caller-supplied order; exact byte match; completed-File expiry from 1 hour through 30 days; no checksum validation, provider dispatch, or accounting |
| Batches | Constrained, gateway-owned | Four official SDK methods over creating-key-owned resources; `batches:manage` plus the endpoint scope; one same-key Batch File with 1–4 non-streaming `/v1/responses`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/moderations`, `/v1/images/generations`, or `/v1/images/edits` requests for one public model; local routing/accounting; strict native legacy Completion validation; fixed-vector Embeddings; native-only Moderations and Images without fallback; Image Edits accept HTTPS or bounded PNG/JPEG/WebP data URLs and reject provider `file_id`; standard endpoint output; aggregate usage only when terminal Batch usage is complete; 24-hour processing expiry; separate success/error output Files; no provider Batch discount, limits, or rate pool |
| Vector Stores | Constrained, gateway-owned | Store lifecycle, atomic create-with-files, file attachment and file-batch SDK methods; same-key ownership, bounded attributes, aggregate bytes/counts, expiry, retained-resource ceilings, validated pagination, UTF-8/ASCII, BOM-marked UTF-16, HTML, DOCX, PPTX, XLSX, and bounded selectable PDF text, backend lexical search, and bounded Responses `file_search`; static token chunking, PDF OCR/scanned text, legacy Office parsing, and embedding-backed semantic search remain pending |
| Additional OpenAI and provider-owned Batches | Pending | Exact vendor-owned video endpoints, provider file references, and provider Batch execution need their own compatibility contracts; the gateway's provider-neutral media-job API is listed below |
| DALL-E 2 edits | Deferred | OpenAI marks [DALL-E 2](https://developers.openai.com/api/docs/models/dall-e-2) deprecated; the gateway avoids extra routing complexity for its legacy edit contract |
| Deprecated OpenAI Videos compatibility | Intentionally omitted | OpenAI schedules the deprecated Videos API to shut down on September 24, 2026; durable video generation uses the provider-neutral `/api/v1/media/jobs` contract instead of adding a dying wire alias |
| `GET /realtime?model={public_model}` | Constrained, native OpenAI target only | Bidirectional HTTP/1.1 WebSocket proxy with `realtime:connect`, public/upstream `realtime`, 16 MiB frames, 10-minute sessions, pre-upgrade routing/admission, an upstream query limited to the rewritten model, and terminal token accounting when `response.done` reports usage; WebRTC, SIP, sideband/fork resources, and cross-provider translation remain pending. `/live/sessions` is intentionally absent because its client secret permits traffic that bypasses gateway limits and accounting. |
| `GET /live` | Constrained, native OpenAI target only | Primary Live WebSocket proxy compatible with the official `session.start` flow; routing and admission use its public `session.model`, the first event is rewritten to the upstream model, bidirectional audio/text/delegation events pass through, public model aliases are restored in session events, and delegated Responses token usage is accounted when reported. WebRTC `/live/sessions`, SIP, stored-session controls, and sideband/fork resources remain intentionally absent because direct provider media would bypass gateway limits and accounting. |
| Fine-tuning and evaluations | Pending | Administrative provider resources need ownership, polling, and cost controls |
| Legacy Assistants, Threads, and Runs | Pending | No compatibility alias is exposed |

## Provider-neutral media namespace

Base URL: `/api/v1/media/jobs`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| Create, list, retrieve, cancel | Constrained, gateway-owned | Durable key-owned jobs with `media:generate`, model/connection grants, a fixed route for provider affinity, encrypted input/output, bounded transient poll retries, explicit `interrupted_unknown` recovery after an unconfirmed dispatch, shared request/concurrency/body/spend admission, and admin-redacted dashboard views |
| Replicate predictions | Implemented | Model, version, and deployment prediction create/poll/cancel contracts; provider-specific input/output stays JSON |
| Together video | Implemented | Video create/poll contract; cancellation stops local polling because the reviewed provider contract has no video cancel operation |
| Gemini Veo | Implemented | `predictLongRunning` create/poll contract through a fixed Gemini `media_jobs` model; simple input is wrapped as one instance or native instances pass through; terminal provider response is encrypted; cancellation stops local polling and cost remains unknown |
| Custom scripted media jobs | Constrained | A trusted owner/admin JavaScript request/response transform maps create/poll/cancel and normalizes `{id,status,output,cost_usd?}` while the gateway retains destination, credential, egress, timeout, size, ownership, and accounting control; the embedded VM has execution and I/O limits but shares the server heap |

## Anthropic client namespace

Base URL: `/api/anthropic/v1`.

| Resource or operation | Status | Boundary |
| --- | --- | --- |
| `GET /models`, `GET /models/{id}` | Implemented, gateway-owned | Native Anthropic list/detail shape over visible public models |
| `POST /messages` | Implemented | JSON translates across capable families; SSE requires an Anthropic target so input usage is correct in `message_start`, whose model is normalized to the requested public alias |
| `POST /messages/count_tokens` | Native target only | The gateway never estimates a different provider's tokenizer result |
| Message Batches | Constrained, gateway-owned | Six official SDK operations over key-owned local resources; 1–4 ordinary non-streaming Messages per 16 MiB submission; current creating-key grants and shared routing/accounting; 24-hour processing expiry; whole-resource deletion with JSONL results after 29 days; no provider-owned 100,000-item ceiling or native batch discount |
| Files | Constrained, gateway-owned | Current GA upload/list/metadata/delete and uploaded-content download rejection; `files:manage`, creating-key ownership, encrypted local PDF/UTF-8 text/JPEG/PNG/GIF/WebP up to 8 MiB, 30-day default or 1–90-day expiry, 1–100 item cursor listing; up to 32 same-key document/image `file_id` references expand to native inline content before Messages/count_tokens routing and count against 16 MiB request/body/token admission; native Anthropic target required; no provider workspace IDs, container uploads, generated-file downloads, `ids[]`, legacy Files beta shape, or no-expiry storage |
| Prompt caching controls | Implemented for native Anthropic Messages | Fixed Anthropic preset plus public/upstream `prompt_cache`; bounded ephemeral controls; JSON/SSE cache-token accounting; no translated or post-dispatch fallback; cache content is never persisted; cost remains unknown until cache rates are versioned |
| Hosted web search | Constrained, native Anthropic preset only | One basic `web_search_20250305`, dynamic-filtering `web_search_20260209`, or dynamic-filtering-with-response-inclusion `web_search_20260318` plus ordinary application tools and optionally one web fetch tool; JSON or SSE; required 1–4 `max_uses`; version-aware direct/code-execution callers and boolean `strict`; `full`/`excluded` `response_inclusion` only on `web_search_20260318`; optional mutually exclusive domain controls and approximate location; public/upstream `chat` and `web_search`, plus `web_search_dynamic` for code execution and `web_search_response_inclusion` for response inclusion; `chat:generate` plus `messages:web_search`, and both `messages:web_*` scopes when combined with fetch; native result/citation/continuation/error/caller shapes, public-model normalization in JSON and `message_start`, cumulative terminal search/code-execution usage, and reported calls; a combined request preserves both `web_search_requests` and `web_fetch_requests` against independent ceilings; prompt caching may be combined with a tool-definition breakpoint and preserves cache counters; no deferred loading, translation, post-dispatch fallback, or free-only/lowest-cost routing; standalone search supports strict spend with a configured per-call fee and cache-read rate, while combined fetch and prompt caching remain unpriced under spend policies |
| Hosted web fetch | Constrained, native Anthropic preset only | One basic `web_fetch_20250910`, dynamic-filtering `web_fetch_20260209`, dynamic-filtering-with-cache-bypass `web_fetch_20260309`, or dynamic-filtering-with-cache-bypass-and-response-inclusion `web_fetch_20260318`, optionally alongside one web search tool; JSON or SSE; required 1–4 `max_uses` and approximate 1–16,384 text `max_content_tokens` that does not bound binary PDFs; version-aware direct/code-execution callers and boolean `strict`; `use_cache` only on the cache-bypass versions and `full`/`excluded` `response_inclusion` only on `web_fetch_20260318`; optional citations and mutually exclusive ten-host domain controls; public/upstream `chat` and `web_fetch`, plus `web_fetch_dynamic` for code execution, `web_fetch_cache_bypass` for cache bypass, and `web_fetch_response_inclusion` for response inclusion; `chat:generate` plus `messages:web_fetch`, and both `messages:web_*` scopes when combined with search; native text/PDF result, caller and error blocks, public model, terminal token/fetch/code-execution usage; provider-reported token pricing unless combined with search or cache creation, which keep cost unknown; prompt caching may be combined with a tool-definition breakpoint and preserves cache counters; no deferred loading, token/spend policies, free-only/lowest-cost routing, Message Batches, translation, or post-dispatch retry |
| Anthropic standalone code execution, computer/browser use, and connectors | Pending | These tools require distinct execution, continuation, capability, security, and billing contracts |
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
| `GET /live` | Constrained, native Gemini target only | Native `BidiGenerateContent` WebSocket relay; official first `setup.model` is resolved and rewritten, provider credentials remain server-side, bidirectional audio/video/text/tool frames pass through, 16 MiB frames and ten-minute sessions apply, and cumulative token usage is recorded when reported |
| Generated images | Native target only | Native `generateContent` image response parts pass through unchanged for capable Gemini models; no cross-provider image-wire translation is claimed |
| Veo video generation | Constrained, gateway-owned lifecycle | Submitted and polled through `/api/v1/media/jobs` with fixed provider affinity, encrypted key-owned output, bounded retries, local cancellation, and honest unknown cost |
| Cached content | Pending | Needs provider affinity, expiry, ownership, and cost accounting |
| Files and resumable media upload | Constrained, gateway-owned | SDK resumable start/upload/finalize (1–8 MiB), encrypted one-hour upload sessions, encrypted 48-hour key-owned Files, list/get/delete, and opaque local File URIs; up to four references/8 MiB combined are expanded under the 16 MiB request cap before native Gemini generation or token counting; Interactions, remote URIs, scanning, downloads, and provider-side Files are not supported |
| Batch generation | Pending | Needs the shared durable-job contract |
| Tuning, permissions, and corpora | Pending | Administrative/resource APIs need separate authorization and lifecycle design |
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
