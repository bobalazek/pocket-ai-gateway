# 2026-09-14 — System and history databases

ID: ADR-003 · Status: accepted direction; implementation contract selected · Source: owner's request for two databases and optional remote libSQL/Turso

**Context.** Rich local logging should not contend indefinitely with key/configuration/budget enforcement. Two databases also remove cross-database transaction guarantees.

**Decision.** Default to system.db and data.db as local SQLite databases. system.db owns users, settings, credentials, grants, quota/budget state, minimal authoritative request/attempt usage, and a bounded event outbox. data.db owns rich history/events/captures and analytical projections. Admission, reservation, and authoritative usage stay in one system transaction.

Deliver an opt-in database/sql-compatible remote libSQL/Turso mode, selected explicitly per store. Remote primary transactions must pass the same contract tests. Default local mode starts offline. No eventual-sync replica is an authority for live limits; no automatic fallback from remote authority to stale local state.

**Consequences.** Data-store events are idempotent projections, with visible delivery lag/failure and a bounded durable outbox. There are no cross-store foreign keys or assumed atomic commits. Backup uses a shared snapshot generation/watermark. Remote outages fail closed where authoritative state is needed; remote mode still supports only one active gateway writer.

Turso currently distinguishes its newer database engine from libSQL, with different Go drivers. Verify each supported engine/driver explicitly rather than calling them universally interchangeable. [Turso Go documentation](https://docs.turso.tech/sdk/go/quickstart)
