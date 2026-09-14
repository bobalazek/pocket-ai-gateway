# 2026-09-14 — Concurrency and coordination

ID: ADR-012 · Status: accepted direction; implementation details delegated · Source: owner's single-process and locking follow-up

**Context.** The gateway will serve concurrent requests and streams, but extra goroutines, connection pools, or Redis can create unbounded work and split authoritative state.

**Decision.** Keep the default deployment self-contained. Go's HTTP server owns request goroutines. Add background goroutines only as bounded, cancellable workers with joined shutdown. Store cross-restart queues, leases, limits, and accounting in SQLite; keep only disposable caches and in-flight state in memory.

Phase 1 uses one connection per database to prevent accidental in-process writer contention. Later phases may add a small read pool while preserving one short write path and never holding a database transaction across provider I/O. Redis is not a default dependency.

**Consequences.** The instance lock preserves one process writer, while SQLite remains the durable coordination authority. A future clustered mode requires a separate decision, distributed fencing, and failure tests rather than silently adding Redis to the local product.
