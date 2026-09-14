# Architecture

Status: Phase 1 foundation implemented; later feature architecture remains the selected design. See [decision register](../project/decisions/README.md).

## Runtime

Use a Go modular monolith. One process owns two storage connections, admission state, provider transports, maintenance work, management API, and the embedded Next.js dashboard.

~~~mermaid
flowchart LR
    Browser[Dashboard] --> Management[Management API]
    Client[OpenAI / Anthropic / Gemini client] --> Protocol[Protocol namespaces]
    Protocol --> Gateway[Shared gateway admission and execution]
    Gateway --> Routing[Authorized target selection]
    Routing --> Adapters[Provider adapters]
    Adapters --> Providers[Upstream providers]
    Management --> Services[Identity and configuration services]
    Services --> DB[(System store)]
    Gateway --> DB
    DB --> Outbox[Bounded event outbox]
    Outbox --> History[(History store)]
    Ops[Backup / retention / recovery] --> DB
    Ops --> History
~~~

The diagram describes responsibilities, not independently deployed services. There is no internal RPC, Redis, queue service, or plugin host.

## Stack and reuse

| Concern | Initial choice | Why / introduction rule |
| --- | --- | --- |
| Server/router | Go net/http and ServeMux | Required routes do not need a framework |
| Logs | log/slog | Structured logs without another runtime system |
| CLI | flag.FlagSet per implemented command | Add a CLI framework only if nested command/help handling becomes cumbersome |
| Storage | database/sql + reviewed local SQLite / optional remote libSQL or Turso drivers | Local by default; certify each backend/engine |
| SQL | sqlc-generated typed queries from explicit SQL | Precise transactions with less scan boilerplate; no GORM/auto-migrate |
| Migrations | Embedded ordered SQL/checksums per store | Transactional per store; paired recovery, no cross-store atomicity claim |
| Passwords | Argon2id from a maintained Go crypto package | Human-password hashing must not be invented |
| Sessions | Maintained Go server-session package with reviewed SQLite store | Evaluate one small library in phase 2; no JWT refresh-token subsystem |
| Provider secret encryption | Standard Go AEAD, versioned envelope | Random nonces, authenticated connection/secret identity |
| Backup encryption | Maintained established encrypted archive format/library | No custom archive cipher or KDF format |
| Locking | Small maintained cross-platform OS-lock implementation | No PID-file-only locking |
| Dashboard | Next.js App Router + strict TypeScript | Recommended static export for the Go-only production runtime |
| UI behavior | Accessible primitives, minimal locally owned components | Reuse proven dialog/menu behavior; do not build a component framework |
| Styling | Tailwind with semantic tokens | Define only tokens/components used by shipped screens |
| Browser routes/data | Prebuilt Next.js screen routes, query-string record IDs, one typed API client | Centralize same-origin credentials, CSRF, cancellation, safe errors, and retry policy; add query caching only when needed |
| API schema | OpenAPI for /api/v1/ | Generate a typed client once the first management slice is stable |
| Testing | Go testing/httptest; focused browser tests | Native harness for transport, persistence, and concurrency |

Go embeds asset files at build time; modernc provides a pure-Go SQLite implementation. Node/build tooling belongs to development and releases. [Go embed](https://pkg.go.dev/embed), [modernc SQLite](https://pkg.go.dev/modernc.org/sqlite)

The owner selected Next.js. Use output: export, basePath /_, and browser-fetched authenticated data for the single-runtime build. Go serves the exported pages/assets; dynamic APIs stay in Go. Request-time SSR/server actions would require a separate runtime decision. [Next.js static exports](https://nextjs.org/docs/app/guides/static-exports)

Resolve currently supported dependency versions from official registries at implementation time, commit lockfiles/checksums, and audit licenses. Do not copy dated version numbers from the planning brief. Release artifacts must include their actual Go, driver, and SQLite engine versions.

## Feature-oriented repository shape

Create directories when their first real behavior is implemented, not as empty scaffolding.

~~~text
cmd/pocket-ai-gateway/main.go
internal/
  app/          construction, lifecycle, process configuration
  server/       listener, global middleware, route registration
  features/
    openaicompat/    OpenAI routes, wire codecs, streams, upstream adapter, fixtures
    anthropiccompat/ Anthropic routes, wire codecs, streams, upstream adapter, fixtures
    geminicompat/    Gemini routes, wire codecs, streams, upstream adapter, fixtures
    auth/           setup, login/logout, sessions, activation, recovery
    users/          profiles, roles, suspension, user grants
    keys/           key lifecycle, grants, rotation
    providers/      provider catalog and connection configuration
    models/         public models, targets, capabilities, catalog refresh
    routing/        strategies, target eligibility, route preview
    limits/         configurable key/user/instance/connection policies
    usage/          reservations, ledger, price versions, historical repricing
    requests/       rich history, event projections, captures
    operations/     local/S3 backup, restore, import/export, diagnostics
  gateway/      request/attempt lifecycle, policy admission, retries
  storage/      system/data migrations, sqlc queries, backend connectors, outbox
web/            Next.js source and embedded out/ production assets
api/            management OpenAPI document
testdata/       sanitized deterministic protocol fixtures
docs/
~~~

Module boundaries are ownership boundaries. Merge a package if it becomes a trivial wrapper. Interfaces belong at actual substitutable boundaries: upstream transport, clock for policy tests, and storage transactions if needed. Do not create interfaces for every struct.

Each compatibility feature owns its HTTP handlers, request/response/error types, SSE codec, upstream format adapter, and contract fixtures. Shared gateway orchestration receives adapters through explicit construction and never imports concrete compatibility features. Keep shared wire codecs as leaf packages where necessary to avoid handler → gateway → adapter import cycles.

Gateway-owned resource APIs live under /api/v1/: /models, /providers, /connections, /keys, /requests, /usage, and /me; privileged instance management lives under /api/v1/admin/. Compatibility APIs live only under /api/openai/v1/, /api/anthropic/v1/, and /api/gemini/v1beta/. Paths determine wire format; routing selects the provider independently.

Feature-local SQL queries and generated methods stay near the feature; storage owns driver setup, per-store migration execution, and transaction primitives. Dashboard source is likewise organized by feature under web/src/features, with thin Next.js page entries.

| Owner | Owns | Depends on / must not own |
| --- | --- | --- |
| app | Startup, process lock, cancellation, worker shutdown | Constructs concrete modules; no business decisions |
| auth/users/keys | Login/session/recovery, account lifecycle, grants and key secrets | System storage; no provider dispatch |
| gateway | One inference execution path and request/attempt states | Identity, routing, accounting, protocol, providers; sole retry owner |
| limits/usage | Policy state, reservations, ledger settlement and repricing | System transactions; no protocol response shaping |
| routing | Eligible target order, scoring, circuit/latency observations | Immutable config + current policy facts; cannot override authorization |
| compatibility features/providers | Namespace codecs, shape conversion, bounded upstream I/O and connection configuration | Cannot mint grants, decide retries, or bypass accounting |
| storage | Transaction boundaries, migrations, SQL and snapshots | No provider calls inside database transactions |
| operations | Backups, restore/import, retention jobs | Storage and local filesystem; owner-only mutation paths |
| feature HTTP handlers/web | Validated actions, response rendering, UI | Call shared services; never read stored credentials for display |

## Request flow

1. Apply listener protections: allowed host/origin, header/body limits, request ID, content type, and protocol version. Authenticate the appropriate inference credential. Reject duplicate/conflicting credential headers.
2. Resolve the user and stable key ID; verify active/expiry state and operation scope. Parse a typed dialect-specific request. Reject unsupported features before any billable work.
3. Resolve the public model by authorized stable ID. Construct a configuration snapshot and compatible authorized targets. Native extensions that constrain provider affinity remove incompatible fallback candidates.
4. Select a target using the configured strategy. Compute bounded input/output token and cost reservations using the target's price/capability snapshot. Unknown cost excludes a target under a spending cap.
5. In one short write transaction, recheck live identity/grant/config revisions; admit all request/bucket/period/concurrency policies; create the logical request and first attempt; store cost/token reservations. On failure, roll back all admissions and return a safe error.
6. Release the transaction. Mark the attempt dispatching durably before transport. Replace client credentials with the connection's credentials; send through the egress policy. There is no database transaction spanning the network.
7. Forward a native response or translate through the typed shared subset. Stream incrementally. Maintain first-response/first-token/last-activity timing and bounded parser buffers.
8. On a retryable rejection, settle only what is known, decide fallback under the overall deadline, recheck current permissions, reserve the next attempt transactionally, and dispatch. Logical user/key request quotas are not charged again; connection dispatch quotas are.
9. Settle usage once per attempt, release concurrency leases, and finalize the request. Preserve unresolved reservations when delivery/billing is ambiguous.

Do not hold a global mutex across HTTP calls or streaming. Admission serialization is short and bounded; live streams execute concurrently. Adapters never retry internally.

If state cannot be persisted before dispatch, return a service error without calling upstream. If persistence fails after a provider accepted work, keep the precommitted reservation, halt new admission while unhealthy, and recover the attempt as unknown. Already-delivered bytes cannot be retracted.

## Storage and concurrency

One gateway writer owns the instance. Phase 1 uses one connection per local store to prevent accidental in-process writer contention. When concurrent feature reads exist, add a separate small bounded read pool while retaining one short write path, foreign keys, and a busy timeout on every connection. System transactions atomically contain identity/config revisions, admission, reservations, usage, and event-outbox records. Data-store projections are delivered idempotently; rich logging never becomes the only accounting source.

Optional remote libSQL/Turso backends require their own transaction/migration/backup certification and single-writer authority. No eventual-sync copy or automatic local fallback may authorize live spending. Remote outages/uncertain commits are handled conservatively. See the [data model](data-model.md) for the outbox, remote commit, and fencing rules.

WAL supports readers alongside a single writer and requires a local filesystem. Keep automatic checkpoints initially, monitor WAL growth, and avoid long read transactions. Do not copy a live DB without its consistency protocol. [SQLite WAL](https://www.sqlite.org/wal.html)

The SQLite WAL documentation records a corruption fix in 3.51.3 and selected backports. Phase 1 must verify the bundled engine includes that fix or a later corrected release; checking only the Go module version is insufficient. [SQLite WAL-reset notice](https://www.sqlite.org/wal.html#walreset)

Concurrency leases are coordinated with admission and released on every terminal path. On process restart, old-epoch leases are cleared only after all prior requests are classified as interrupted. Durable request/token/cost counters remain. An administrator lowering a limit below current use prevents new work rather than rewriting history or cancelling completed usage.

All workers have bounded work batches, a cancellation context, one owner, and shutdown joins. Required accounting is synchronous; optional rich telemetry may be dropped with visible counters rather than accumulating unbounded queues.

Go's HTTP server supplies request goroutines. Add goroutines only for independent I/O or bounded workers, always with cancellation, limits, and joined shutdown. Durable queues, leases, quotas, and cross-restart coordination live in SQLite. Memory is limited to disposable caches and in-flight state. Redis is not part of the single-process default; a future clustered mode would require a separate distributed-coordination decision and evidence.

## Routing implementation

Filter first: operation → user/key grants → published/enabled target → feature compatibility → pricing/free-only policy → circuit availability → capacity. Free-only routes require a manager-recorded zero price verified within the previous 24 hours; final admission atomically rechecks the exact price and freshness because either can change after selection. Embedding models use exactly one fixed target so stored vectors never change spaces under the same public ID.

| Strategy | Selection rule |
| --- | --- |
| Fixed | Exactly one eligible target |
| Ordered fallback | Configured order; advance only on eligible failures |
| Weighted | Positive configured weights among eligible targets; renormalize after filtering |
| Lowest estimated cost | Compare a common request/output-bound cost estimate; unknown prices excluded |
| Lowest observed latency | Compare operation-specific aged EWMA measurements, using first token for streams and total time for non-streaming requests |

Initial latency defaults to evaluate with mocks: at least 10 successful samples, a 15-minute freshness horizon, EWMA alpha 0.2, and at most 5% exploration of eligible candidates. Cold/stale pools use configured priority. These are tuning proposals, not measured optimums. Exploration uses real authorized traffic; no automatic paid probes.

Track failure rate separately from speed. Three consecutive retryable failures open a 30-second target circuit; successful traffic clears it. Rate-limit Retry-After and connection authentication faults have distinct handling. Cap weights/time arithmetic and test deterministic seeded sampling. Record strategy/config revision, observation age, selected score, cold-start/exploration status, and all attempts.

Never compare arbitrary different models as equivalent quality. Operators opt into each target in a public model. Embeddings remain fixed. “Fastest” means observed under this instance's traffic, not a guaranteed global fastest provider.

## Configuration and runtime reload

The configured system store is the sole application-configuration source. Typed configuration writes update a monotonically increasing revision and audit in one transaction. The runtime publishes a validated immutable snapshot only after commit. Before each attempt, revocation, current grants, connection disablement, and relevant revisions are checked again.

Flags → environment → defaults controls listen/data-dir/TLS/proxy/secret-file process settings. Those require restart where documented. Import/export is a versioned representation, not a second live source.

Config edits use optimistic concurrency: stale revision returns 409 with reload guidance. Provider tests and route previews are explicit operations. Free/catalog/price changes never automatically expand permissions.

## Architectural validation

Embedded-asset, local-store, locking, migration, accounting, protocol, translation, routing, and catalog-refresh behavior is covered by deterministic local tests. Remote stores, S3 recovery, and release artifacts require their separate operational certification.

Phase 1 evidence is recorded in its phase specification. No provider compatibility guarantee, remote-driver certification, release benchmark, or security audit is claimed yet.
