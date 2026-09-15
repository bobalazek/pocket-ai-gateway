# Pocket AI Gateway — product requirements

Version 0.3 · September 14, 2026 · Build specification incorporating owner decisions

## 1. Product brief

Pocket AI Gateway is an MIT-licensed, self-hosted AI gateway for developers and small teams who want to share provider connections while controlling access, usage, routing, and cost. One Go executable contains the HTTP server and a statically exported Next.js dashboard. Mutable state defaults to two local SQLite databases; remote libSQL/Turso storage is an optional deployment mode.

An operator starts the server, claims it, adds providers, publishes stable model names, creates users and scoped application keys, and connects applications through OpenAI, Anthropic, or native Gemini API formats. The gateway translates requests and responses to the selected capable provider. The dashboard explains every attempt and how much usage/cost is known, estimated, or unresolved.

The reason to build is operational simplicity: replace application-specific credential distribution, provider switching, and disconnected usage logs with one small service. Without it, each application continues owning those concerns. The differentiator to validate is the complete install-to-request-and-recovery experience, not the number of provider logos.

PocketBase is the experience reference: embedded database, authentication, dashboard, and a standalone application. This project does not depend on PocketBase or reproduce its general collection APIs. [PocketBase documentation](https://pocketbase.io/docs/)

## 2. Scope and release boundaries

| Stage | Meaning |
| --- | --- |
| Foundation | Internal builds of runtime, storage, identity, and policy. Not a usable public gateway. |
| First usable alpha | Phases 1–5: users/admins, scoped keys, configurable persistent controls, OpenAI/Anthropic/Gemini native and translated generation, tools, streaming, embeddings, translated stateless Responses, history/dashboard, offline recovery. |
| Feature-complete beta | Phase 6: initial provider presets, editable catalog, all initial routing strategies, and their dashboard surfaces. |
| v0.1 stable | Phase 7: beta plus remote-store certification, delayed usage pricing, local/S3 encrypted backups, restore, security/compatibility verification, Docker/platform distributions, documentation, and MIT notices. |
| Follow-up | Phase 8: additional provider certifications/native adapters and optional self-update; separately scoped advanced features. |

Multiple local users are included. Organizations, workspaces, subscriptions, enterprise SSO, clustering, Redis/PostgreSQL requirements, model hosting, agent/tool execution, MCP servers, vector storage, semantic caching, and a consumer chat product are excluded from v0.1.

Full fidelity of the three client protocols is the product goal. v0.1 covers the generation, tool, stream, counting, and embedding operations specified below; provider-specific features require tested mappings or explicit unsupported-capability errors. Phase 8 contains the remaining API-parity work: state/resource ownership, files/batches/caches, hosted tools, multimedia/realtime, and related provider-specific surfaces. Do not advertise whole-API parity until that inventory is complete and tested.

Image input and strict structured output are required on capable, tested route combinations. Provider-affine reasoning/signature data must be preserved or rejected explicitly; it must never be silently dropped. Durable background Responses are a distinct delivery mode from streaming and use the gateway-owned contract in ADR-015.

“One executable” describes distribution, not a single source file or a database embedded inside the executable. WAL creates auxiliary files. Users must preserve the entire data directory through the documented backup procedure.

## 3. Actors and ownership

One instance is one trust domain, with exactly one active owner and additional admins/members as needed.

| Actor | Capabilities and limits |
| --- | --- |
| Owner | All administration, owner transfer, master-key/backup policy, restore, updates, and recovery. Cannot remove the last active owner. |
| Admin | Manage member accounts, provider/model configuration, grants, keys, limits, metadata, and operational diagnostics. Cannot promote admins, alter the owner, restore the instance, or retrieve stored secrets. |
| Member | Own profile/sessions, keys within administrator-assigned grants, own usage and request history, permitted models, and playground. Cannot expand grants, budgets, or body-capture policy. |
| Application | Uses one inference key belonging to an active user; no management API permission. |
| Local operator | Access to the protected data directory authorizes documented offline recovery; possession of the host remains a trust boundary. |

Users are created locally through an activation code; no email service or public registration is necessary. Suspension invalidates sessions and blocks new requests/attempts for all the user's keys. Existing streams may complete; this behavior is disclosed.

## 4. Functional requirements

Every ID below is assigned to implementation phases in the roadmap. Acceptance statements are required behavior, not completed checks.

### RUN-01 — Self-contained runtime

Serve the dashboard at /_/, management API at /api/v1/, and protocol-specific inference at /api/openai/v1/, /api/anthropic/v1/, and /api/gemini/v1beta/ on one listener. Default to 127.0.0.1:8080 and ./pocket_gateway_data. Resolve and display the absolute data path. Allow explicit listen/data-dir flags and corresponding environment settings.

Start offline, initialize migrations and protected key material, acquire an OS-backed instance lock, and shut down gracefully. No provider health check should prevent access to local configuration. Compiled assets must work when source/build directories are removed.

**Acceptance:** a clean machine can launch the binary without Node.js, Python, a separate database, or a cache; a second process refuses the same data directory; unknown API paths return API errors rather than the dashboard.

### IAM-01 — Users and secure access

On the first dashboard visit, an instance without an owner enters a resumable setup flow. Require a short-lived, single-use setup code shown through the local process before creating the owner; never ship a pre-created account or default credentials. Use password hashing designed for human passwords, revocable server-side sessions, login throttling, CSRF/origin protection, secure cookie handling, and offline owner recovery. Activation/recovery codes expire and are stored as verifiers.

Enforce the actor matrix on the server for every resource and field. Role changes, account suspension, recovery, and credential operations are audited. Members never receive other users' records through lists, details, exports, counts, or indirect identifiers.

**Acceptance:** duplicate setup attempts cannot create two owners; two members cannot read or alter each other's keys/history; a suspended member cannot authenticate or dispatch; the last owner cannot be disabled.

### KEY-01 — Application keys and grants

Create, label, expire, disable, revoke, and rotate high-entropy keys; show plaintext once and store only a verifier plus non-secret prefix. Every key belongs to a user. Rotation preserves logical key identity, counters, and grants, with explicit overlap/expiry if supported.

Scopes: chat:generate, responses:generate, embeddings:generate, moderations:classify, models:read, tokens:count. Effective access intersects user grants, key grants, public-model publication, allowed connections, capability support, and all limits. Empty grants deny; unrestricted access requires an explicit administrator-controlled mode.

Members may create narrower keys, never raise their own ceiling. New keys share the user's aggregate quota. Public model IDs are stable; deleting and recreating a public name does not inherit grants.

**Acceptance:** rotation cannot reset spend or quotas; raw model IDs, fallback, model discovery, and alternate authentication headers cannot bypass grants; revocation blocks new attempts immediately.

### PROV-01 — Provider connections

Manage multiple connections per provider, credentials/secret references, base URL, explicit local-network permission, timeouts, and approved custom headers. UI-entered secrets are encrypted and never readable in full after saving. Missing external secrets disable the connection visibly.

Initial stable integrations: native OpenAI, Anthropic, and Gemini, plus OpenRouter, generic OpenAI-compatible, and Ollama. Gemini's OpenAI-compatible interface is an additional preset, not a substitute for native Gemini client/provider support. A named preset is supported only for operations verified in its published matrix.

Tests distinguish configuration validation, model discovery, and potentially billable inference. Do not issue billable test prompts automatically.

**Acceptance:** cloud and local providers use the same policy/accounting path; changing connections never returns credentials to browsers; failures identify a safe actionable category.

### MODEL-01 — Catalog, custom models, and publication

Separate provider-specific upstream models from public model names. A public model maps to one or more connection/model targets. Register custom models manually, discover candidates where supported, and seed versioned provider/model metadata from releases.

Track supported operations, input/output modalities, tools, streaming, structured output, context/output limits, and prices with source/version/check time. Distinguish configured, discovered, and verified capability evidence.

Discovery never grants access or publishes models. Refreshes show a diff and preserve local overrides. Models disappearing upstream become unavailable without silently remapping aliases. Support an optional periodic refresh of bounded versioned catalog data from this project's configured GitHub source, with last-check/version/error visibility. Fetch data only; never execute remote code.

Free models require explicit zero pricing with provenance and freshness. Unknown price is not zero. A free-only route must reject paid fallback, unknown/stale price, and unverified extra fees. Label self-hosted zero provider cost separately from hardware/electricity cost.

**Acceptance:** a custom model works without a code change when the protocol/capabilities are supported; a refresh cannot change published routes or grants without an explicit change; free-only routes never select a known paid target.

### API-01 — OpenAI, Anthropic, and Gemini interfaces

Expose OpenAI Chat Completions/Responses/Responses input-token counting/embeddings/moderations/non-streaming image generation/buffered speech/models under /api/openai/v1/, Anthropic Messages/count_tokens/models under /api/anthropic/v1/, and native Gemini generateContent/streamGenerateContent/countTokens/embedContent/batchEmbedContents/models under /api/gemini/v1beta/. The path selects the protocol; headers never switch dialects. Global aliases are disabled by default. Preserve each dialect's response/error/event shape without a gateway JSON envelope.

Support ordinary text, multi-turn system/user/assistant content, function tool calls/results, and incremental SSE. Each of the three client formats can reach all three provider families for supported operations. OpenAI includes both Chat and Responses translation. Capability mismatches fail explicitly; no route silently discards requested semantics.

Reject unsupported fields or combinations before dispatch. Capability checks include streaming, tools, strict output, images, reasoning, and output limits. Native provider-specific data is preserved only on allowlisted/tested paths. Never turn reasoning or tool blocks into ordinary text.

**Acceptance:** pinned official OpenAI, Anthropic, and Google Gen AI SDK fixtures pass across the nine client/provider family combinations, with additional Chat/Responses cases; incompatible features return actionable client-dialect errors and cause no upstream call.

### API-02 — Stream and tool correctness

Preserve event order, tool IDs, content-block indexes, partial tool arguments, usage events, and stop reasons. Bound parsing buffers and event sizes; implement downstream backpressure and cancellation. Distinguish connect, first-response, idle, and overall deadlines.

Never retry after downstream headers or events are committed. Never splice two providers' outputs or invent a successful completion after truncation. Forward tool requests/results only; the caller executes application tools.

**Acceptance:** split UTF-8, fragmented JSON, multiple tool calls, missing final usage, slow consumers, malformed events, and client disconnects have deterministic tested outcomes without unbounded memory.

### API-03 — Embeddings and Responses boundaries

Embeddings preserve indexes, dimensions, encoding, and batch order; token-ID inputs require a tested native path. Each public embedding model has one target and a versioned vector-space contract. A different embedding model requires a new public model or an explicit breaking change.

Responses supports native and translated POST /api/openai/v1/responses across the three provider families, including typed output items, function tools/results, and stream lifecycle events. Non-streaming responses default to gateway-owned storage under the creating API key; `store:false` disables the stored Response resource. Durable background execution uses the same ownership contract. Gateway-owned Conversations attach to synchronous, background, and bounded buffered-stream Responses as separate retained state; failed terminal streams leave them unchanged. Provider-owned response or conversation chains and hosted tools require separate phase 8 contracts. Opaque provider-affine inputs require a matching route or explicit rejection.

**Acceptance:** embeddings never silently change vector space; unsupported Responses state receives an explicit error; local request history is never used as a substitute for provider conversation state.

### LIMIT-01 — Buckets and quotas

Each API key can choose fixed-window or refillable token-bucket rate policies, with explicit window length or capacity/refill rate, independently for requests/model tokens. Also support independent quotas per UTC hour, day, ISO week (Monday start), calendar month, and lifetime. Instance/user/connection ceilings also apply. All applicable policies must pass.

Also support global/user/key/connection concurrency, maximum body size, batch size, output tokens, and absolute key expiry. No unbounded queue: reject capacity exhaustion with a protocol-appropriate 429 and retry timing when knowable.

Persist rate/quota state. Admission is atomic across policies; a rejected request does not partially consume another policy. Count an accepted logical request once for instance/user/key quotas, and each dispatch for connection quotas. Billable token consumption covers all attempts. No automatic refund of admitted request quotas after provider failure.

Document continuous bucket semantics separately from fixed UTC quota boundaries. Lifetime quotas have no automatic reset. Rotation, relabeling, import, restart, or log retention cannot reset allowance.

**Acceptance:** concurrent admissions cannot spend the same allowance twice; tests cover period boundaries, restart, clock rollback, rotation, aggregate user limits, and fallback charging.

### COST-01 — Usage and spending caps

Provide USD spending caps over hour/day/week/month/lifetime periods at the same policy scopes. Use fixed-point/decimal-safe monetary values and versioned price snapshots. Track provider-reported, estimated, and unknown usage separately, including cached input, output/reasoning counters, and additional known fees without double counting.

Reserve a conservative amount before each upstream attempt in the same database transaction as admission. Reconcile idempotently when usage arrives; keep uncertain reservations after crashes/disconnects until reconciliation or audited adjustment. A capped route without a bounded estimate or trustworthy price is ineligible.

Display these as gateway-estimated spending limits. They cannot guarantee the provider's invoice or prevent already-running upstream work from exceeding an estimate. An overrun becomes visible debt that prevents new admissions under that cap.

**Acceptance:** every attempt has its own cost state; fallback reserves again; overlapping caps settle consistently once; unknown usage is never silently recorded as free.

### ROUTE-01 — Strategies and forwarding

Implement fixed target, ordered fallback, weighted selection, lowest estimated request cost, and lowest observed latency. Eligibility filtering always precedes scoring. Recheck live revocations/disablement before each new attempt.

Latency routing uses bounded, aged observations for comparable connection/model/operation/streaming combinations, including time to first token and total duration. Explicit exploration/cold-start behavior, circuit breaking, and deterministic mock evaluation are required. No quality claim follows from low cost or latency.

Fallback is opt-in, with bounded attempts, a shared deadline, and retry backoff/jitter. Default maximum is two attempts when enabled. Only verified rejection/transient cases are retried; invalid input, policy denial, authentication failure, safety refusal, and ambiguous accepted work are not default triggers.

Expose an administrator route preview and per-request selection reason, rejected candidates, timing, and attempts. Aggregator restrictions cover the aggregator connection; guarantees about its downstream providers require separately tested upstream routing controls.

**Acceptance:** a prohibited target never wins any strategy; an unhealthy fast target does not dominate; a partial stream never falls back; preview sends no inference.

### DATA-01 — Durable state and history

SQLite owns users/sessions, encrypted credentials, settings, model publication, grants, quota/budget state, requests/attempts, usage, and audits. Commit required state before upstream dispatch; if required persistence fails, stop admitting work.

Metadata records include actor/key, public model, client dialect, configuration revision, attempt targets, timestamps, status/error class, latency, usage/cost provenance, and tool-call count. Tool names, arguments, results, prompts, vectors, and responses are content and require explicit capture.

Body capture is off by default, capped, access-controlled, and independently retained. Default request metadata retention: 30 days or 100,000 requests, whichever limit is reached first. Daily aggregates: 365 days. Captured content: at most 7 days by default when enabled. Accounting needed by active/lifetime limits and unresolved reservations is independent of log retention.

**Acceptance:** pruning requests does not reset spend; interrupted work remains unknown after restart; a disk-full error cannot become an apparent successful durable write.

### DATA-02 — Two stores and optional remote libSQL/Turso

Default system.db owns identity/configuration, atomic admission and authoritative usage/accounting. Default data.db owns rich request/attempt/tool events, content capture, and analytics. Move events through a bounded durable outbox with idempotent delivery; show logging lag/failure. Cross-store transactions and foreign keys are not assumed.

Support explicit optional remote libSQL/Turso backends per store after transaction, migration, failure, and backup certification. Default local mode remains offline-capable. A remote authoritative store outage stops admission; never fall back to stale local balances. No multi-writer/cluster promise.

**Acceptance:** a data-store outage cannot reset limits or lose authoritative accepted usage; outbox saturation is bounded and visible; local and remote backend contract tests pass; a paired backup restores consistent store generations.

### COST-02 — Delayed pricing and historical recalculation

Persist raw/normalized usage even when pricing is unavailable. Show cost as N/A. Later effective-dated prices can calculate historical requests using the price applicable to their model/provider/time, with a preview and audited, idempotent ledger adjustments. Keep original estimates and as-recorded costs alongside restated values.

Observational keys may route unknown-priced usage without a monetary admission guarantee. Strict spend-capped keys require known/provisional bounded prices and reserve before dispatch. Historical adjustments update original periods/lifetime totals; a discovered overrun blocks future admissions but cannot undo past spend.

**Acceptance:** repeated repricing never double-charges; current prices are not blindly applied to old requests; raw usage survives; periods, provenance, adjustments, and unresolved records remain explainable.

### UI-01 — Embedded dashboard

Provide setup/login/activation, overview, users, providers, models/routes, keys/limits, requests/attempts/tools, usage/spend, playground, audit, settings/backup/version, and account views. The persistent sidebar places the signed-in account at the bottom with email, password, session, and logout controls. Members see scoped variants; no UI filter substitutes for server authorization.

Use Tailwind CSS and source-owned shadcn components for the dashboard. Usage charts use shadcn Chart with Recharts and retain accessible tabular summaries; do not encode meaning by color alone.

All actions use the management API; browsers never hold provider credentials. The playground uses an explicitly selected ordinary inference key, stored in memory only, and warns about billable tests.

Provide server pagination, useful empty/error/conflict states, keyboard access, focus management, labelled fields, readable contrast, and mobile/tablet layouts. All assets load locally.

**Acceptance:** keyboard-only setup/key creation/revocation works; two member sessions see isolated data; deep links and offline assets work; no secret is persisted in browser storage.

### SEC-01 — Security boundaries

Validate inbound sizes/types, authentication, resource ownership, and allowed fields. Strip inbound credentials/cookies/hop-by-hop headers before egress. Only privileged configuration can select provider URLs, proxies, or sensitive headers.

Protect against SSRF through destination validation at connection and dial time, metadata-address denial, bounded redirects, and explicit private-network allowlists for local providers. Do not fetch user-supplied media URLs in the gateway.

Protect sessions and secrets, restrict CORS/trusted proxies, expose minimal health information, and audit privileged changes without logging secret values. No telemetry or automatic outbound maintenance by default.

**Acceptance:** malicious headers, redirects, DNS changes, hidden model IDs, role escalation, CSRF, and secret-bearing errors are covered by negative tests.

### OPS-01 — Configuration, backups, and restore

Keep application configuration in typed SQLite tables; flags/environment only configure process settings. Versioned JSON export excludes secrets/sessions by default. Imports validate, preview, and transact; cannot overwrite counters or broaden grants silently.

Provide protected local snapshots and passphrase/recipient-encrypted portable backups containing both stores and required encryption material. Support manual and scheduled local/S3-compatible backups, with configurable nightly schedule, retention, retries, integrity verification, and visible failure status. Restore validates a consistent store generation before replacing data and preserves a recovery path on failure.

**Acceptance:** a clean installation restores users, grants, models, limits, history, and decryptable credentials; wrong passphrase, corrupt archive, invalid schema, disk-full, and interrupted restore leave the existing instance recoverable.

### OPS-02 — Distribution and upgrades

Ship and smoke-test Linux/macOS amd64/arm64 and Windows amd64 binaries, plus Linux amd64/arm64 Docker images with persistent volumes and non-root execution. Advertise other platforms only after testing.

Versioned releases include checksums, signature/provenance verification, dependency notices/SBOM, compatibility results, release notes, and migration instructions. Upgrades retain the data directory, snapshot before schema changes, and reject unsupported newer schemas. Rollback restores a matching backup and binary.

Manual executable replacement and Docker image replacement are v0.1 requirements. Self-update is phase 8: explicit invocation, authenticated artifacts, supported platform swap, restart/health check, rollback. No automatic in-process mutation of a running server.

**Acceptance:** upgrade and rollback are demonstrated on real artifacts; failed migrations do not silently modify usable data; Docker recreation with the same volume preserves state.

## 5. Non-functional targets

Targets are unmeasured engineering goals. Benchmark on a documented 2-vCPU/2-GB Linux host with local SSD and deterministic mock providers, with durable accounting enabled.

| ID | Target |
| --- | --- |
| NFR-01 | First request within five minutes for a new operator with valid provider credentials |
| NFR-02 | Existing small instance starts within two seconds excluding migrations |
| NFR-03 | Idle RSS below 100 MiB with small configuration and capture off |
| NFR-04 | Fifty concurrent text streams with bounded memory/goroutines and responsive management APIs |
| NFR-05 | p95 added gateway overhead below 20 ms at 50 requests/second for 1-KiB non-streaming payloads |
| NFR-06 | All security, revocation, admission, and accounting writes survive tested restart/failure scenarios |

Measure gateway overhead separately from upstream latency. Record hardware/filesystem, payloads, policy/capture settings, DB size, throughput, latency percentiles, memory, and cancellation behavior. A failing target triggers profiling and an explicit scope/target review, not weakened durability.

## 6. Definition of done

v0.1 is complete when a person can download one executable or run its Docker image, securely create users/admins, connect the initial providers, publish models, issue scoped keys, use the documented OpenAI/Anthropic/Gemini interfaces, enforce persistent configurable quotas/spending controls, reprice historical usage, understand routing/tool activity, choose local or certified remote storage, and back up locally or to S3, restore, upgrade, and roll back.

Release is blocked by authorization bypass, secret exposure, known data-loss behavior, quota races, unsafe retries, unsupported compatibility claims, or an untested restore path. The [roadmap](../plan/README.md) defines evidence for each phase and the final release.
