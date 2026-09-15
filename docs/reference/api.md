# API namespaces and compatibility contract

Management and compatibility contract. OpenAI, Anthropic, and native Gemini clients use separate namespaces with request, response, tool, usage, and streaming translation to the selected capable upstream.

The management API is machine-readable in [openapi.yaml](openapi.yaml). Each inference namespace has its own OpenAPI document: [OpenAI](openapi-openai.yaml), [Anthropic](openapi-anthropic.yaml), and [Gemini](openapi-gemini.yaml).

## Route hierarchy

| Surface | Root | Purpose |
| --- | --- | --- |
| OpenAI compatibility | /api/openai/v1 | OpenAI Chat, Responses, embeddings, models, errors, and stream formats |
| Anthropic compatibility | /api/anthropic/v1 | Anthropic Messages, token counting, models, errors, and stream formats |
| Gemini compatibility | /api/gemini/v1beta | Native Gemini generation, streaming, counting, embedding, and model formats |
| Gateway resources | /api/v1 | Gateway-owned providers, connections, public models, keys, usage, requests |
| Auth feature | /api/v1/auth | Setup, activation, login, logout, and session/recovery operations |
| Administration | /api/v1/admin | Privileged users, policies/settings, audits, imports, backups, diagnostics |
| Dashboard | /_ | Embedded Next.js pages and assets |

Protocol roots are /api/openai, /api/anthropic, and /api/gemini. Each retains its native versioned path below that root. The path is authoritative; headers never select a different dialect. Unprefixed /v1/messages, /v1/chat/completions, and ambiguous global inference aliases are not enabled by default.

The prefix names the **client API specification**, not a forced upstream provider. /api/anthropic/v1/messages may target Gemini or OpenAI through translation, and its response remains Anthropic-shaped. Gateway metadata at /api/v1/models has its own schema and is distinct from all three compatible model-list endpoints.

## Operation inventory

| Endpoint | Wire contract / initial behavior |
| --- | --- |
| POST /api/openai/v1/chat/completions | OpenAI request, choices/tool calls/usage, errors, incremental chat chunks; `store:true` creates a key-owned 30-day local resource and requires non-streaming output |
| GET /api/openai/v1/chat/completions; GET/POST/DELETE /api/openai/v1/chat/completions/{id} | List, retrieve, update metadata, or delete gateway-stored Chat Completions for the creating key |
| GET /api/openai/v1/chat/completions/{id}/messages | Cursor pagination over the stored original Chat input messages |
| POST /api/openai/v1/responses | OpenAI input/output items, function tools/results, typed response lifecycle stream, or durable background submission |
| POST /api/openai/v1/responses/compact | Native OpenAI compaction for direct model/input requests; no cross-provider approximation |
| POST /api/openai/v1/responses/input_tokens | Native OpenAI input-token count for direct model/input requests; no provider-owned references |
| POST /api/openai/v1/images/generations | Native non-streaming image generation; explicit public model and `images:generate` scope; no automatic fallback, strict token/spend/free-only policies, or lowest-cost routing |
| POST /api/openai/v1/images/edits | Native non-streaming multipart GPT Image edits; 1-16 validated PNG/JPEG/WebP inputs under 50 MB each, optional same-size PNG mask under 4 MB, 64 MiB aggregate body, 16 MiB response, explicit public model, prompt, and `images:edit`; DALL-E 2 edits, automatic fallback, strict token/spend/free-only policies, and lowest-cost routing are unsupported |
| POST /api/openai/v1/images/variations | Native multipart variation from one square PNG under 4 MB; explicit public model and `images:variation`; OpenAI preset restricted to upstream `dall-e-2`, custom compatible endpoints operator-declared; 64 MiB body and 16 MiB response bounds; no automatic fallback, strict token/spend/free-only policies, or lowest-cost routing |
| POST /api/openai/v1/audio/speech | Native built-in-voice text-to-speech with buffered audio up to 16 MiB; explicit public model and `audio:speech`; no custom voice references, SSE, automatic fallback, strict token/spend/free-only policies, or lowest-cost routing |
| POST /api/openai/v1/audio/transcriptions | Native multipart audio transcription; one supported audio file up to 25 MB, 16 MiB non-streaming response limit, explicit public model and `audio:transcribe`; provider fields and streaming pass through; no automatic fallback, strict token/spend/free-only policies, or lowest-cost routing |
| POST /api/openai/v1/audio/translations | Native multipart audio-to-English translation; one supported audio file up to 25 MB, 16 MiB response limit, explicit public model and `audio:translate`; the OpenAI preset requires upstream `whisper-1`, while custom compatible endpoints are operator-declared; provider fields pass through; no streaming, automatic fallback, strict token/spend/free-only policies, or lowest-cost routing |
| GET/DELETE /api/openai/v1/responses/{id} | Creating-key retrieval and deletion of gateway-owned stored Responses |
| POST /api/openai/v1/responses/{id}/cancel | Cancels a queued or in-progress background Response |
| GET /api/openai/v1/responses/{id}/input_items | Bounded cursor pagination over the stored original input |
| POST /api/openai/v1/conversations | Create a key-owned local conversation with optional initial items |
| GET/POST/DELETE /api/openai/v1/conversations/{id} | Retrieve, update metadata, or delete a key-owned conversation |
| POST/GET /api/openai/v1/conversations/{id}/items | Add or paginate gateway-owned conversation items |
| GET/DELETE /api/openai/v1/conversations/{id}/items/{item_id} | Retrieve or remove one gateway-owned item |
| POST /api/openai/v1/embeddings | OpenAI embedding inputs/indexes/encoding/dimensions/usage |
| POST /api/openai/v1/moderations | Native OpenAI moderation request and taxonomy; no streaming or cross-provider translation |
| GET /api/openai/v1/models and /models/{id} beneath that root | OpenAI model list/detail; authorized public models |
| POST /api/anthropic/v1/messages | Anthropic message/content blocks, stop reasons, usage, typed content stream |
| POST /api/anthropic/v1/messages/count_tokens | Target-appropriate counting; no invented exact cross-model token count |
| GET /api/anthropic/v1/models and /models/{id} beneath that root | Anthropic model list/detail/pagination |
| POST /api/gemini/v1beta/models/{model}:generateContent | Gemini contents/parts, candidates, finish/safety metadata, usageMetadata |
| POST /api/gemini/v1beta/models/{model}:streamGenerateContent?alt=sse | Native Gemini incremental response envelopes |
| POST /api/gemini/v1beta/models/{model}:countTokens | Native-shaped token-count result for the selected target |
| POST /api/gemini/v1beta/models/{model}:embedContent | Gemini embedding shape mapped to a compatible embedding target |
| POST /api/gemini/v1beta/models/{model}:batchEmbedContents | Bounded embedding batch with per-input mapping/order |
| GET /api/gemini/v1beta/models and /models/{model} beneath that root | Gemini names/pagination/capabilities for authorized public models |

The model name in Gemini's path and the model field in other protocols resolve to the same stable published-model object. No raw upstream name bypasses publication.

Official schema references: [OpenAI Chat](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions), [OpenAI stored Chat list](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions/methods/list), [OpenAI stored Chat messages](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions/subresources/messages/methods/list), [OpenAI Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses), [OpenAI Responses input tokens](https://developers.openai.com/api/reference/resources/responses/subresources/input_tokens/methods/count), [OpenAI Moderations](https://developers.openai.com/api/reference/resources/moderations/methods/create), [OpenAI Images](https://developers.openai.com/api/reference/resources/images/methods/generate), [OpenAI Speech](https://developers.openai.com/api/reference/resources/audio/subresources/speech/methods/create), [OpenAI Transcriptions](https://developers.openai.com/api/reference/resources/audio/subresources/transcriptions/methods/create), [OpenAI Translations](https://developers.openai.com/api/reference/resources/audio/subresources/translations/methods/create), [Anthropic API](https://platform.claude.com/docs/en/api/overview), [Gemini generation](https://ai.google.dev/api/generate-content), [Gemini token counting](https://ai.google.dev/api/tokens), [Gemini embeddings](https://ai.google.dev/api/embeddings).

## SDK base URLs and authentication

| Client | Intended base URL configuration | Gateway credential |
| --- | --- | --- |
| OpenAI SDK | http://127.0.0.1:8080/api/openai/v1 | Authorization: Bearer gateway key |
| Anthropic SDK | http://127.0.0.1:8080/api/anthropic; SDK appends /v1/messages | x-api-key gateway key; supported anthropic-version |
| Google Gen AI SDK | Root http://127.0.0.1:8080/api/gemini with API version v1beta | x-goog-api-key gateway key |

Pin SDK versions and assert the final emitted URL in tests; option names/version-appending behavior are documented against those versions. Browser sessions and management tokens never authorize these inference routes.

Support native authentication forms within their namespace. If Gemini SDK/REST compatibility accepts a key query parameter, strip/redact it before every access log, trace, error, and capture; prefer the header in all examples. Reject duplicate/conflicting credentials. A protocol/version header mismatch returns that namespace's error, never switches codecs.

Replace gateway credentials with the selected upstream credentials. Strip inbound cookies, hop-by-hop headers, forwarding data, and untrusted header/transport overrides. Error/capture redaction occurs before storage. Return a safe gateway request-ID header without wrapping compatible response bodies.

## Translation obligations

Every generation cell below has deterministic request/response coverage. Feature-specific restrictions still apply and are rejected before dispatch when translation cannot preserve them.

| Client format | OpenAI-family upstream | Anthropic upstream | Native Gemini upstream |
| --- | --- | --- | --- |
| OpenAI Chat | Native/compatible | Translated | Translated |
| OpenAI Responses | Native stateless | Translated stateless | Translated stateless |
| Anthropic Messages | Translated | Native | Translated |
| Gemini generateContent | Translated | Translated | Native |
| OpenAI Moderations | Native only | Unsupported | Unsupported |
| OpenAI image generation | Native only | Unsupported | Unsupported |
| OpenAI text-to-speech | Native only | Unsupported | Unsupported |
| OpenAI audio transcription | Native only | Unsupported | Unsupported |
| OpenAI audio translation | Native only | Unsupported | Unsupported |

This is nine protocol-family paths, plus OpenAI's distinct Chat/Responses cases. Embeddings and moderations run only on providers/models implementing those native operations; Anthropic text models do not become embedding or moderation targets. Counting uses the target's tokenizer/provider capability or returns an explicit unsupported operation.

Required cases for each eligible path:

- Ordinary text, multi-turn context, system instructions, output bounds, and stop/finish semantics.
- Function declarations, tool choice, multiple tool calls/results, stable IDs/correlation, and fragmented streaming arguments.
- Incremental text/tool events, terminal state, usage, cancellation, timeouts, refusals, malformed upstream responses, and provider failures.
- Structured output and image input on capable targets, with explicit native semantic mappings.
- No silent dropping of reasoning, safety controls, signatures, cache directives, or unsupported parameters.

Gemini's contents/parts, systemInstruction, functionCall/functionResponse, candidate finish/safety fields, and usageMetadata require dedicated codecs. Never treat Gemini as just another OpenAI-compatible hostname. Preserve opaque thought signatures on supported native/affine paths; translated paths must preserve valid semantics or reject them. [Gemini function calling](https://ai.google.dev/gemini-api/docs/function-calling), [Gemini thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures)

The internal shared representation contains only faithfully representable messages/parts/tools/results/usage/events. Native opaque extensions remain typed and capability/provider-affine. Adapters never bypass shared key grants, limits, accounting, or destination controls.

## Delivery, streams, and failures

Ordinary JSON responses and SSE are supported delivery modes. SSE is not a durable background job. `background:true` with storage enabled submits to a bounded SQLite queue; polling, cancellation, deletion, retention, and input-item access remain restricted to the creating API key. The queue admits at most 4 active jobs or 16 MiB per key, 16 jobs or 32 MiB per owner, and 32 jobs or 64 MiB for the instance. Retained unexpired Responses are separately capped at 250/128 MiB per key, 2,500/512 MiB per owner, and 10,000/1 GiB per instance; active jobs reserve their maximum response size. One joined worker rotates across owners and keys and rechecks current grants and routing before dispatch. Restarted in-progress work becomes an unknown terminal failure and is never redispatched.

Parse SSE incrementally with bounded event/response sizes, CRLF/multi-line data/heartbeat support, arbitrary chunks, and split UTF-8. Gemini streamed candidate envelopes, OpenAI chat chunks, and Responses lifecycle events each have their own codec. Anthropic streaming requires an Anthropic-compatible target until another target can provide accurate input usage before `message_start`; cross-provider Anthropic generation is JSON-only. [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)

Do not retry once downstream headers/events are committed. Truncation never emits an invented success terminal event. Midstream errors follow the selected protocol where representable, then close; no normal JSON error is written into an active stream. Client disconnect cancels upstream promptly but may leave billable usage unknown.

Connect, first-response, idle, downstream-write, and overall deadlines are separate. Preserve tool ordering/call IDs and finish reasons; validate completed argument JSON without requiring each fragment to parse.

| Condition | HTTP / semantic treatment |
| --- | --- |
| Invalid input/unsupported semantics | 400, namespace-native error with safe field detail, no dispatch |
| Invalid inference key/suspended owner | 401, no identity disclosure |
| Scope denied | 403; hidden models use uniform 404 |
| Unknown/hidden model | 404 with indistinguishable safe detail |
| Body too large | 413 before unbounded buffering |
| Rate/quota/concurrency/spend ceiling | 429 with protocol code and Retry-After only when meaningful |
| No eligible target/storage unavailable | 503; safe retry guidance, no internal topology |
| Upstream timeout/unusable response | 504/502; distinguish client from provider faults |
| Provider safety refusal | Preserve native/translated refusal semantics; no retry to evade it |

OpenAI errors use its error object; Anthropic uses its typed error envelope; Gemini uses its native code/message/status/details structure with unsafe details removed. Management errors use the gateway's own schema.

## Embeddings, Responses, and broader parity

Embedding translation preserves indexes, vector dimensions, encodings, task settings, and model contract. One public embedding model has one vector-space target; do not fallback to an unrelated model. Token-ID input and batch capabilities require tested target compatibility.

Responses translation includes output items, tool-call/results, usage, and lifecycle events. Non-streaming responses are stored by default for 30 days under the creating API key and can be retrieved, cancelled while active, inspected for input items, or deleted through the OpenAI namespace. The gateway assigns the public response ID and sends `store:false` upstream so provider storage is never implied. Client `store:false` requests support JSON and lifecycle SSE without creating a stored Response; attaching a Conversation still updates that separate retained resource after success. Provider-owned response chains, hosted tools, files, and caches remain separate resource contracts.

Response compaction is forwarded only to the native OpenAI preset. The gateway rewrites the public model to the selected upstream model and accounts returned usage. Direct `model` and inline `input` are required; response, conversation, item, file, and container references are rejected because resolving provider-owned resources with a shared credential would violate gateway ownership. [OpenAI compact reference](https://developers.openai.com/api/reference/java/resources/responses/methods/compact)

Conversation resources are stored locally under the creating API key. Create, retrieve, metadata update, deletion, item addition, item retrieval, item deletion, and cursor pagination match the official resource shapes for messages, function calls, and string function outputs. Provider-owned item/file/container references, other item types, and `include` projections are rejected. Global, owner, key, and per-conversation caps bound retained data; deleted content is purged after 30 days. Synchronous JSON, durable background, and `store:false` streaming `POST /responses` requests may identify a conversation by string ID or `{id}`: local history is prepended for any target family, local IDs never leave the gateway, and successful new input plus completed output commit in one transaction. Background jobs persist the conversation snapshot through queue restarts; stale history becomes a failed Response without a partial turn. Attached streams are bounded to 16 MiB and replay a successful terminal event after the turn is stored, trading incremental delivery for persistence integrity. A provider `response.failed` or `error` terminal is replayed without changing the Conversation. [OpenAI Conversations reference](https://developers.openai.com/api/reference/python/resources/conversations/methods/create)

Full API fidelity is the goal, not a blanket claim at alpha. Maintain a complete official endpoint/field inventory for all three specs: mark each implemented/tested, native-only, translated, pending, or inherently unavailable for a target. Assign files, batches, cached resources, provider-hosted tools, multimodal/realtime, and remaining stateful surfaces to explicit phase 8 work. Do not call rejected or unimplemented operations fully supported.

## Management API

Base path /api/v1/. Server-side session cookies authorize browser operations; separate scoped management tokens may authorize CLI/automation. Roles and ownership apply to both. Inference credentials never authorize this API.

| Resource/action | Methods and representative paths | Permission / important behavior |
| --- | --- | --- |
| Setup | GET /auth/setup/status, POST /auth/setup/claim | Status exposes only the claim requirement; the first same-origin claim atomically creates the sole owner |
| Current session | GET /auth/session | Implemented in Phase 1; safe current-owner fields from the HttpOnly server-side session |
| Activation | POST /auth/activate | One-time code; limited session until password chosen |
| Sessions | POST /auth/login, POST /auth/logout, GET/DELETE /auth/sessions | Own sessions, CSRF on cookie mutations |
| Password/profile | GET/PATCH /me, POST /auth/password | Reauthenticate for sensitive changes; revoke sessions as appropriate |
| Recovery | POST /admin/users/{id}/recovery-code; offline owner-reset CLI | Privileged one-time recovery; no unauthenticated account enumeration |
| Users | GET/POST /admin/users, GET/PATCH /admin/users/{id}, POST /admin/users/{id}/activation-code or /recovery-code | Owner/admin; admin limited to members |
| Owner transfer | POST /admin/owner/transfer | Owner + recent authentication; transactional invariant |
| Grants | Included in user reads; PUT /admin/users/{id}/grants | Owner/admin within role limits; reductions permanently narrow existing keys |
| Keys | GET/POST /keys, GET/PATCH /keys/{id}, POST /keys/{id}/rotate, DELETE /keys/{id} | Own narrower keys for members; secret returned once |
| Connections | GET/POST /connections, GET/PATCH/DELETE /connections/{id} | Owner/admin; archive/disable where referenced |
| Provider types | GET /providers | Gateway provider/adapter catalog; safe capability metadata |
| Credential replacement | PUT /connections/{id}/credential | Write-only secret/reference; no reveal endpoint |
| Provider tests/discovery | POST /connections/{id}/test, POST /connections/{id}/discover | Explicit test mode; billable mode requires scoped inference key |
| Upstream catalog/prices | GET/POST/PATCH catalog resources under /connections/{id}/models | Candidate data; price versions immutable after use |
| Public models/routes | GET/POST /models, GET /admin/models, GET/PUT /admin/models/{id}/route | Owner/admin mutations; member reads limited public projection; fixed and embedding routes use one target; free-only needs manager-recorded zero pricing verified within 24 hours |
| Route preview | POST /admin/models/{id}/route-preview | Owner/admin; representative operation, stream mode, and token estimates; no dispatch |
| Policies | GET /keys/{id}/effective-limits, GET/POST /admin/policies, PATCH /admin/policies/{id} | Owner/admin writes; member reads own effective limits |
| Requests/attempts | GET /requests | Server ownership filter; source/target route, usage, declared-tool count, returned-tool-call count, and completion state; no prompt or argument capture |
| Captured content | GET /requests/{id}/content | Separate opt-in permission and audit |
| Usage/unknowns | GET /usage, GET /usage/unresolved, POST /admin/usage/adjustments | Scoped reads; audited privileged adjustments |
| Repricing | POST /admin/usage/reprice-preview, POST /admin/usage/reprice | Bounded synchronous preview and idempotent historical adjustments |
| Unknown reconciliation | POST /admin/usage/reconciliations | Audited token/cost facts that release uncertain reservations |
| Catalog refresh | GET/PUT /admin/catalog, POST /admin/catalog/refresh | Configured GitHub source, bounded data only, scheduled refresh off by default; candidates never publish automatically |
| Audit/settings | GET /admin/audit, GET/PATCH /admin/settings | Privileged views; owner-only secret/backup/security policy fields |
| Import/export | POST /admin/config/preview, POST /admin/config/import, GET /admin/config/export | Owner; versioned, redacted, transactional |
| Backup jobs | GET/POST /admin/backups, GET /admin/backups/{id} | Local/S3 destination; owner and recent authentication for artifact access |
| Diagnostics/version | GET /admin/diagnostics, GET /version | Safe authenticated metadata; minimal public health separately |

OpenAPI must fully define fields, required/optional distinctions, ownership, read/write-only secrets, limits, pagination, and errors for each implemented slice. This route inventory is not a completed OpenAPI spec.

List responses use data, next_cursor, and has_more; bounded limit defaults to 50 and caps at 200. Cursors include a stable sort key and are validated/scoped. Money is a decimal string; timestamps are UTC RFC 3339.

Configuration writes carry a revision/If-Match precondition. Duplicate create submissions support a bounded idempotency key where needed; do not persist plaintext key secrets for replay. If a key-create response is lost, list/revoke the unusable credential and rotate explicitly.

Setup, user/grant mutations, imports, and multi-policy updates transact with their audit record. Dangerous operations show a preview in the UI, but server authorization remains mandatory.

## Provider rollout and certification

| Provider family | Adapter boundary |
| --- | --- |
| OpenAI | Native Chat, stateless Responses, and embeddings |
| Anthropic | Native Messages/count_tokens and shared-subset translation |
| Google Gemini | Native client/upstream API plus shared-subset cross-format translation |
| OpenRouter | OpenAI-compatible preset; downstream-provider guarantees require separately configured OpenRouter controls |
| Ollama | OpenAI-compatible local preset with explicit private-network access and no credential requirement |
| Generic OpenAI-compatible | Configurable endpoint with explicitly selected capabilities |
| Azure OpenAI | OpenAI-compatible v1 preset with a validated resource URL and `api-key` authentication |
| Amazon Bedrock | OpenAI-compatible runtime or Mantle resource URL with a Bedrock bearer API key; SigV4 is outside this preset |
| Google Vertex AI | OpenAI-compatible regional or global resource URL with a Google Cloud access token; token refresh remains operator-managed |
| Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, Perplexity | Built-in OpenAI-compatible presets pin reviewed endpoints and advertised operations; deterministic adapter conformance is tested, while live certification is tracked separately |

Gemini, OpenRouter, and Ollama document compatible surfaces, but expose different features. Their inclusion is a testing commitment, not an assumption of native parity. [Gemini compatibility](https://ai.google.dev/gemini-api/docs/openai), [OpenRouter quickstart](https://openrouter.ai/docs/quickstart), [Ollama compatibility](https://docs.ollama.com/api/openai-compatibility)

Do not hardcode a “latest” model in a protocol adapter. Bundle small versioned catalog entries/presets with provenance, allow operator overrides, and treat remote discovery as untrusted candidate metadata.

Each certification records date, pinned SDK/version, endpoint, adapter version, configured model ID/revision, feature, native/translated path, expected result, limitations, and test evidence. Deterministic mocks run in ordinary CI. Real-provider smoke tests require configured credentials and a small explicit cost ceiling; secrets are unavailable to untrusted pull requests.

The current evidence and provider-specific boundaries are recorded in [provider certification](../project/provider-certification.md).

Do not claim general coding-agent compatibility from one chat request. Such a claim needs the specific client's tool, reasoning, Responses, stream, and resource behavior exercised end to end.
