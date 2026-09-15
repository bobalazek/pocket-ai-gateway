# Data model and accounting invariants

Logical schema for implementation. Phases 1–2 migrations cover the local stores, identity/session lifecycle, API keys, and audit events; later rows below remain planned. Governed by ADR-001, ADR-003, ADR-004, and ADR-005 in [decisions](../project/decisions/README.md).

## Conventions

Use two stores: system.db for identity/configuration and authoritative admission/accounting, and data.db for rich history and analytical projections. Both default to local SQLite; each can explicitly use a certified remote libSQL/Turso backend. Settings holds only small typed instance settings that do not need relational ownership.

IDs are stable, opaque, randomly generated identifiers. Public names are mutable presentation/API aliases, never authorization IDs. Timestamps are UTC integer milliseconds internally and RFC 3339 in management responses. Durations use explicit units.

Enforce foreign keys within each store. Cross-store IDs are validated by services and retained as snapshots; do not pretend SQLite can enforce cross-store/remote foreign keys. Use unique constraints for identities, prefixes/selectors, aliases, period keys, and settlement idempotency. Parameterize SQL and generate typed methods with sqlc.

Money uses signed 64-bit USD nano-units internally, with checked overflow and decimal string inputs/outputs; prices per million tokens use decimal-safe calculation and conservative rounding up for reservations. Unknown is nullable with a provenance/status field, never zero. Store provider raw usage counters separately from normalized totals so overlapping cache/reasoning fields are not added twice.

## Entity census

Unless marked below, the following tables live in the system store. Minimal requests, attempts, raw usage, and the ledger are authoritative system records, even though rich request history lives in data.db.

| Tables | Important fields / relationships | Ownership and lifecycle |
| --- | --- | --- |
| users | id, normalized login, display name, password hash, role, status, auth revision, timestamps | Instance; pending_activation → active ↔ suspended → archived; unique normalized login |
| user_sessions | id/verifier, user_id, expiry, last activity, auth revision | User; revoke on password recovery, role change, or suspension |
| activation_tokens | verifier, user_id/purpose, expiry, creation time | One-time activation/recovery; consumption deletes the verifier; no plaintext persistence |
| management_tokens | selector/verifier, user_id, allowed management scopes, expiry/revocation | Issued explicitly; privileges capped by current user; never an inference key |
| provider_connections | id, name, adapter, normalized base URL, enabled, egress rules, timeouts, config revision | Instance; owner/admin manage; disable before archive |
| provider_secrets | connection_id, secret kind, ciphertext/nonce/key version OR external reference | Connection; no readable-secret management endpoint |
| upstream_models | id, connection_id, upstream identifier, capability JSON, evidence source/check time, active | Private catalog; unique connection + upstream ID |
| price_versions | id, upstream_model_id, currency, decimal rates, fee rules, source, effective/check times, expiry | Immutable once referenced by an attempt; explicit zero allowed |
| public_models | id, unique public name, operation family, strategy, enabled, revision, embedding contract | Instance; grants bind ID; archive leaves historical snapshots |
| route_targets | id, public_model_id, upstream_model_id, priority, weight, enabled | Model; explicit list, unique target per public model |
| user_model_grants / user_connection_grants | user_id + resource_id; user operation scopes | Administrator ceiling; empty means deny |
| api_keys | id, owner_user_id, label, state, expiry, scope set, revision | Logical key; active ↔ disabled → revoked; immutable owner |
| api_key_secrets | key_id, unique selector, verifier, expiry/revocation | One or briefly overlapping rotated credentials; no quota identity here |
| key_model_grants / key_connection_grants | key_id + resource_id | Must remain within current user grants |
| limit_policies | id, scope kind, one applicable scope FK, metric, algorithm, period, capacity/refill/cap, revision | Typed policies; exactly one scope or instance, not an unconstrained foreign ID |
| bucket_state / rate_windows | policy_id, remaining/refill state OR window start/count, policy revision | Persistent selectable bucket/fixed-window algorithms |
| quota_periods | policy_id, period_start/end, consumed, reserved | Unique policy + period start; lifetime uses a single non-expiring period |
| reservations | id, attempt_id, policy_id, period_id, reserved units, state | Unique attempt + policy; active → settled/released or uncertain |
| concurrency_leases | request_id/attempt_id, scope, process_epoch | Request leases for user/key/instance; attempt leases for connection |
| requests | id, owner_user_id, key_id, operation/dialect, public_model_id + name snapshot, config revision, timing/outcome | One logical inbound operation; terminal summary can survive target archive |
| attempts | id, request_id, ordinal, target/connection/model snapshots, price_version, state, timings, total/cache/output usage provenance, nullable web-search call ceiling/count | One upstream dispatch intent; unique request + ordinal; hosted-search count is separate from token usage and has unknown cost; retention keeps the non-content ceiling/count so historical usage remains visible and ordinary two-rate repricing cannot absorb hosted-search attempts |
| stored_responses | gateway response ID, creating key/user, model, request/result JSON, queue state, lease epoch, associated usage request ID, timestamps | Creating key; 30-day retention; queued → running → completed/failed/cancelled, with interrupted work terminal and never replayed |
| stored_chat_completions | gateway completion ID, creating key/user, public model, request/result/metadata JSON, timestamps | Creating key; 30-day retention; shares global, owner, and key count/byte ceilings with stored Responses |
| stored_chat_completion_metadata | completion ID, metadata key/value | Normalized predicate index for bounded stored Chat Completion list filters; cascades with its parent resource |
| conversations | gateway conversation ID, creating key/user, metadata, created/deleted timestamps | Creating key; active until explicit deletion; deleted rows and items are retained for 30 days |
| conversation_items | conversation ID, gateway item ID, ordinal, item JSON, created timestamp | Parent conversation and creating key; 20 items per append, with global, owner, key, and per-conversation storage bounds |
| usage_ledger | id, attempt_id, entry type, measured units/cost, adjustment link, idempotency key | Append-only settlements/adjustments; unique reconciliation event |
| usage_daily | date + relevant user/key/model/connection dimensions, aggregate total/cache/output and web-search-call counters | Data store; rebuildable summaries, not enforcement source; provider search fees are not inferred from call count |
| captured_content | request/attempt snapshot IDs, encrypted bounded payload, size, expiry | Data store; opt-in, no credentials, independently deleted |
| audit_events | actor, action, resource ID, redacted before/after, time, request ID | Instance security record; bounded retention, never plaintext secrets |
| settings / schema_migrations | typed settings/revision; migration version/checksum | Instance; schema changes only through versioned migrations |
| backup_jobs | id, state, artifact metadata/checksum, key version, timestamps/error category | Owner-scoped local job status; no passphrase or remote secret |
| event_outbox | monotonic event ID, request/attempt IDs, redacted bounded payload, delivery status | System store; transactional with required state, replayable |
| request_details / attempt_events / tool_events | unique event ID, request/attempt/user snapshots, event/timing fields | Data store; idempotent projections, redacted rich observability |
| pricing_jobs | range/model/filter, selected price versions, totals, status | System store; audited idempotent bounded repricing runs |
| cost_assessments | attempt ID, price version, assessment kind, amount/delta, effective time, idempotency key | System store; original and restated cost history |

Both stores have their own schema_migrations table. No authoritative counter exists only in data.db.

## Store delivery and remote-mode contract

Commit minimal accepted request/attempt/usage records and a redacted event in the system transaction. A bounded worker copies events into data.db with a unique event ID, then acknowledges delivery in system.db. A crash between write and acknowledgment causes safe duplicate delivery, not duplicate usage.

Keep the outbox byte/count limits visible. A data outage can delay rich history while system accounting continues until the configured outbox ceiling; at that ceiling fail new admissions cleanly instead of growing without bound or losing required events. Raw content capture has its own bounded success/failure policy and is never silently described as complete.

Use one database authority per store. Local mode uses local SQLite transactions. Remote mode uses the configured primary's verified transaction semantics, with safe ambiguity handling and idempotent writes; a network-uncertain commit must be resolved by ID before retrying or dispatching. Stale replicas/sync copies never authorize spending.

Remote system storage requires a tested single-writer lease/fencing epoch, because a local directory lock does not prevent a second gateway elsewhere connecting to the same remote DB. Loss of authority stops new admission. This does not make the gateway a supported cluster. Separate remote targets and credentials are operator configuration; no automated cloud provisioning.

Migration compatibility, transaction atomicity, foreign keys, busy/timeout behavior, integer/JSON types, and backup/export/restore are certification gates for each engine/driver. Similar SQL syntax is not evidence of identical durability.

All-user operational queries are owner/admin only. Member queries constrain owner_user_id in SQL before aggregation, pagination, and count; management code must not load all rows and filter afterward. Key/attempt access resolves the owning request/user. Inference keys cannot access these management tables.

## Authorization and configuration mutations

User-grant reduction takes effect immediately for new attempts even if saved key grants are now broader. Do not silently broaden a key after a user receives new privileges. Member-created keys can only select current explicit grants. Unrestricted mode is a separate, explicit privileged field, not an empty-list convention.

Keep api_keys as the quota identity while rotating api_key_secrets. A new key cannot bypass the user's aggregate cap. Changing key ownership is unsupported in v0.1; create a new key with an audited policy instead.

Archive a user/model/connection when referenced; never cascade away attempts or usage. Once history is pruned, preserve required aggregate/cap state and audit snapshots. Owner transfer is one transaction that verifies an active destination and retains exactly one owner.

## Admission transaction

Calculate the candidate's bounded token/cost estimate outside the transaction. Then, under the serialized write path:

1. Verify the estimate's configuration/price revisions and current account/key/target eligibility.
2. Read or create all relevant policy periods and bucket state using one effective UTC admission time.
3. Check request cost of one logical admission, projected token use, monetary reservations, and all concurrency capacities.
4. If any policy fails, mutate none. Otherwise consume request units, reserve token/spend units, allocate leases, and create request/attempt records atomically.
5. Commit before marking/sending upstream work.

Use a request-level admission only on the first attempt. A fallback creates another attempt and connection admission while retaining the logical request's lease/charge. Every potentially billable attempt needs new token/spend reservations across the relevant scopes.

Reservations bind to each period at attempt admission; late completion settles that same period even after the wall clock moves into a new month. A fallback after a boundary may reserve in the next period. Do not move previous reservations to make new room.

## Policy semantics

| Policy | Rules |
| --- | --- |
| Request bucket | Tokens represent requests, not model tokens; capacity is maximum burst, refill is requests/second |
| Fixed-window rate | Key-selectable request/model-token count over a configured positive window length; persisted window and counters; documented boundary burst |
| Model-token bucket | Conservatively reserve estimated input + bounded output; settle actual usage without overfilling after refill |
| Hour/day/week/month quota | UTC fixed calendar periods; week starts Monday; separate from continuous rate buckets |
| Lifetime quota | No automatic expiry/reset; historical allowance retained independently of log deletion |
| Spend cap | Consumed + unresolved reservations + proposed reservation must fit every applicable cap |
| Concurrency | Request lease lasts through final attempt/cancellation; connection lease lasts through that dispatch |

For token buckets: refill first, cap at capacity, apply measured-versus-reserved adjustment once, then cap refunds at capacity. Overrun may create negative available balance; deny new work until recovered. Quota reservations similarly become actual usage or remain uncertain.

Clock rollback never grants refill or reopens a closed period. Persist the last effective time and use a nondecreasing effective clock across restarts; expose significant clock anomalies. Forward jumps advance periods with an audit/diagnostic warning. UTC quotas rely on a correctly administered host clock; the operator controls that trust boundary.

Policy edits retain policy IDs and consumption. Raising a cap permits more work; lowering it may put an account over limit. Changing the period/algorithm or explicitly resetting a lifetime allowance is an owner/admin audited operation, with current reservations protected. Imports cannot replace counters with zeros.

For an impossible request larger than bucket capacity, 429 explains the ceiling; do not return a fictitious finite Retry-After. A temporary rejection reports the maximum delay among currently blocking rate/period policies. Concurrency and lifetime caps may have no known retry time.

## Attempt lifecycle and crash recovery

~~~text
reserved → dispatching → streaming → succeeded
    |           |            |
    |           +------------+→ failed / cancelled / interrupted_unknown
    +→ cancelled_before_dispatch
~~~

Persist dispatching before network I/O. A crash after that point may represent accepted upstream work even if no response reached the gateway. On restart:

- Reserved work known never dispatched can release token/spend reservations; admitted request counts remain.
- Dispatching/streaming work becomes interrupted_unknown and retains uncertain billable reservations.
- Completed settlement is never applied again; unique ledger/reservation keys make replay idempotent.
- Previous-process concurrency leases are discarded after interruption classification.

There is no transaction spanning SQLite and a provider. Do not promise exactly-once inference, automatic safe replay, or exact billing on every failure.

If actual usage exceeds the estimate, record the full usage, expose the overrun, and block new admissions under exhausted caps. If usage never arrives, preserve the estimate/unknown distinction. Adjustments require an actor, reason, and append-only ledger entry; never edit away the original event.

## Retention and query design

Index user/time, key/time, request/attempt IDs, model/time, status/time, period uniqueness, and active reservation lookups. Use bounded keyset pagination ordered by timestamp plus ID; validate date ranges and aggregation limits.

Prune captures first, then old terminal request detail in bounded batches, after required ledger summaries exist. Do not delete unresolved attempts/reservations or enforcement totals. Long-lived unknowns appear in an operator reconciliation queue, rather than silently pinning unlimited rich payloads.

Default audit retention is 365 days with an explicit count/size cap; quota-reset/security actions retain compact summaries needed to explain active policies. Unpriced raw usage needed for later cost calculation remains in compact system records after rich logs expire; expose that separate retention. All retention settings are visible.

## Delayed price resolution

Usage and price are independent facts. Persist normalized and raw usage with provider/model/time even when no price exists. An observational request can complete with N/A cost; a strict cap cannot admit it without a conservative provisional/known estimate.

Price versions have effective intervals and provenance. A new open-ended successor atomically closes an unused prior interval; retroactive changes that would invalidate a snapshotted attempt are rejected. Historical corrections use explicit preview and repricing. Do not apply today's price retrospectively simply because it is available.

Repricing first previews affected attempts and cap/period impact. Apply a unique assessment per attempt + pricing version + calculation version, append a delta to the ledger, and update the original policy periods and lifetime totals transactionally in system.db. Repeated jobs resume by cursor and cannot double-count.

Preserve as-recorded cost and restated cost separately. Later corrections append new assessments; raw tokens/timestamps and original dispatch decisions never change. Release/adjust a provisional reservation only through that same transaction. If repricing reveals exhausted caps, record debt and prevent new admissions; already completed work cannot be undone.

## Migration and snapshot rules

Migration version/checksum changes are validated before mutation. Back up existing data before schema changes; fail if the snapshot cannot be made. Prefer additive migrations. Rollback uses a matching prior binary and snapshot, not automatic down-migrations.

Snapshot consistency covers both stores, their schema versions, outbox watermark, instance generation, and encryption-key version. Briefly quiesce mutations/dispatch admission, finish or durably record events, and take a paired snapshot; the procedure must also work for each certified remote backend. Serialize key rotation with backup. Configuration-only JSON is not a backup.
