# Implementation context

Read once before implementing the first task. This repository currently contains planning documents only.

The user's constraint is one executable/server with embedded dashboard and local SQLite storage, optionally packaged in Docker. Product scope is [PRD](../../project/prd.md); architectural defaults and open decisions are in [decisions](../../project/decisions/README.md); phase status/coverage is [roadmap](../README.md).

Use Go net/http, explicit dependency construction, two local SQLite stores through database/sql + sqlc, and a statically exported Next.js/TypeScript dashboard. Application configuration and authoritative accounting belong in the system store; rich history belongs in the data store. Optional remote libSQL/Turso remains future certified work. End users need no Node.js server, Redis, or external database. Use maintained security/SQLite dependencies where necessary; add nothing solely for future flexibility.

The [data model](../../architecture/data-model.md) owns table structure; [API contract](../../reference/api.md) owns wire behavior; [dashboard](../../design/dashboard.md) owns interface structure; [operations](../../guides/operations.md) owns recovery procedures.

Compatibility features use /api/openai, /api/anthropic, and /api/gemini roots with native version paths beneath them. Auth is a separate feature at /api/v1/auth; gateway resources/admin are separate. Read the [feature contracts](../../features/README.md) before implementing any of these boundaries.

Rules for each implementation slice:

1. Read the actual code/callers before editing. Use CodeGraph first if an index exists; otherwise repository search. Do not index without a user decision.
2. Keep the task bounded; create a module/directory only when implementing its real behavior.
3. Never bypass shared authorization, admission, retry, or accounting in adapters, tests, or playground.
4. Required state commits before upstream dispatch. Unknown usage remains unknown. No retry after downstream stream commitment.
5. Never delete or weaken a security/accounting regression to make a change pass. Prove a new guard rejects its invalid case.
6. Run the relevant checks and the shared verification gate. Report observed output and unavailable checks accurately.
7. Update the phase record only when its observable acceptance is met. A scaffold or a passing mock does not imply provider certification.

The first task is [F1.1](phase-1-foundation.md). Later phase entries are scoped work packages; decompose them against the current code before execution. This plan does not authorize publishing releases, sending messages, spending on live tests, or installing an update into a deployed instance.
