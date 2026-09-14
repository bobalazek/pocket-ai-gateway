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
| POST /api/openai/v1/chat/completions | OpenAI request, choices/tool calls/usage, errors, incremental chat chunks |
| POST /api/openai/v1/responses | OpenAI input/output items, function tools/results, typed response lifecycle stream |
| POST /api/openai/v1/embeddings | OpenAI embedding inputs/indexes/encoding/dimensions/usage |
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

Official schema references: [OpenAI Chat](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions), [OpenAI Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses), [Anthropic API](https://platform.claude.com/docs/en/api/overview), [Gemini generation](https://ai.google.dev/api/generate-content), [Gemini token counting](https://ai.google.dev/api/tokens), [Gemini embeddings](https://ai.google.dev/api/embeddings).

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

This is nine protocol-family paths, plus OpenAI's distinct Chat/Responses cases. Embeddings run only on providers/models implementing embeddings; Anthropic text models do not become embedding targets. Counting uses the target's tokenizer/provider capability or returns an explicit unsupported operation.

Required cases for each eligible path:

- Ordinary text, multi-turn context, system instructions, output bounds, and stop/finish semantics.
- Function declarations, tool choice, multiple tool calls/results, stable IDs/correlation, and fragmented streaming arguments.
- Incremental text/tool events, terminal state, usage, cancellation, timeouts, refusals, malformed upstream responses, and provider failures.
- Structured output and image input on capable targets, with explicit native semantic mappings.
- No silent dropping of reasoning, safety controls, signatures, cache directives, or unsupported parameters.

Gemini's contents/parts, systemInstruction, functionCall/functionResponse, candidate finish/safety fields, and usageMetadata require dedicated codecs. Never treat Gemini as just another OpenAI-compatible hostname. Preserve opaque thought signatures on supported native/affine paths; translated paths must preserve valid semantics or reject them. [Gemini function calling](https://ai.google.dev/gemini-api/docs/function-calling), [Gemini thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures)

The internal shared representation contains only faithfully representable messages/parts/tools/results/usage/events. Native opaque extensions remain typed and capability/provider-affine. Adapters never bypass shared key grants, limits, accounting, or destination controls.

## Delivery, streams, and failures

Ordinary JSON responses and SSE are initial delivery modes. SSE is not a durable background job. Background submit/poll/cancel needs its own ownership/retention/charging contract and remains a separate clarification.

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

Stateless Responses translation is v0.1 scope, including output items, tool-call/results, usage, and lifecycle events. store:false is the initial supported policy. Persisted response/conversation chains, resource retrieval/delete, background operation, hosted tools, and provider-owned files/caches need the phase 8 resource ownership/affinity design.

Full API fidelity is the goal, not a blanket claim at alpha. Maintain a complete official endpoint/field inventory for all three specs: mark each implemented/tested, native-only, translated, pending, or inherently unavailable for a target. Assign files, batches, cached resources, provider-hosted tools, multimodal/realtime, and remaining stateful surfaces to explicit phase 8 work. Do not call rejected or unimplemented operations fully supported.

## Management API

Base path /api/v1/. Server-side session cookies authorize browser operations; separate scoped management tokens may authorize CLI/automation. Roles and ownership apply to both. Inference credentials never authorize this API.

| Resource/action | Methods and representative paths | Permission / important behavior |
| --- | --- | --- |
| Setup | GET /auth/setup/status, POST /auth/setup/claim | Implemented in Phase 1; status exposes only the claim requirement, and the local code atomically creates the sole owner |
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
| Azure OpenAI, AWS Bedrock, Google Vertex AI | Dedicated authentication/resource/routing probes required before native adapters |
| Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, Perplexity | Compatible adapter only where current official docs and tests establish the claimed operation |

Gemini, OpenRouter, and Ollama document compatible surfaces, but expose different features. Their inclusion is a testing commitment, not an assumption of native parity. [Gemini compatibility](https://ai.google.dev/gemini-api/docs/openai), [OpenRouter quickstart](https://openrouter.ai/docs/quickstart), [Ollama compatibility](https://docs.ollama.com/api/openai-compatibility)

Do not hardcode a “latest” model in a protocol adapter. Bundle small versioned catalog entries/presets with provenance, allow operator overrides, and treat remote discovery as untrusted candidate metadata.

Each certification records date, pinned SDK/version, endpoint, adapter version, configured model ID/revision, feature, native/translated path, expected result, limitations, and test evidence. Deterministic mocks run in ordinary CI. Real-provider smoke tests require configured credentials and a small explicit cost ceiling; secrets are unavailable to untrusted pull requests.

Do not claim general coding-agent compatibility from one chat request. Such a claim needs the specific client's tool, reasoning, Responses, stream, and resource behavior exercised end to end.
