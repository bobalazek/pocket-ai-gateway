# Phase 3 — Admission, buckets, quotas, and spend accounting

Status: implementation complete; verification evidence recorded below. [Plan index](README.md)

Depends on phase 2 identity and phase 1 transactional storage. Use deterministic mock attempts; no paid provider is required.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| C3.1 Policy engine | Per-key fixed-window or token-bucket rates, hour/day/week/month/lifetime quotas, global/user/key/connection policies, body/output/batch/concurrency ceilings; all constraints intersect atomically | LIMIT-01, KEY-01 |
| C3.2 Attempt accounting | Requests/attempts/leases/reservations/ledger, price snapshots, decimal-safe estimates, idempotent settlement; each fallback attempt gets its own reservation | COST-01, DATA-01 |
| C3.3 Recovery/clock | Restart interruption classification, unknown usage queue, protected consumption on rotation/import/pruning, clock/period boundary semantics | LIMIT-01, COST-01, OPS-01 |
| C3.4 Policy API/UI | Preview effective limits, scoped usage summaries, price provenance, safe limit edits and audited adjustments | UI-01, COST-01 |
| C3.5 Historical pricing | N/A usage capture, effective-dated and weekly UTC prices, cache-read rates, preview/reprice jobs, idempotent ledger deltas, original/restated costs and original-period updates | COST-02, DATA-01 |
| C3.6 Store projection | Bounded transactional system outbox to data-store events; idempotent replay, visible lag/full behavior, no accounting loss | DATA-02 |

Frontend: one Usage workspace with visible policy scopes, effective-limit inspection, known/estimated/unknown indicators, and structured limit-denial explanations.

The initial repricing operation is synchronous and rejects ranges over 10,000 attempts. A resumable background cursor is deferred until operating evidence shows the bounded operation is insufficient.

Dashboard charts use a small source-owned shadcn-style wrapper around Recharts v3, with an accessible label and tabular summary. Do not add ApexCharts as a second chart stack.

**Exit gate:** concurrent callers cannot over-admit; denial changes no sibling policy; repeated settlement charges once; month/week transitions and restart preserve allowance; unknown billable work remains reserved; lowering a cap blocks new admissions.

**Blast radius:** costs and access for every future adapter. This phase is a hard dependency of live dispatch, not post-launch hardening.

## Verification evidence

- Unit/integration: checked money arithmetic, bucket carry, period boundaries, concurrent admission, atomic denial, idempotent settlement/repricing/reconciliation, restart recovery, and outbox replay.
- Contract/UI: OpenAPI validation, typed client tests, production dashboard build, embedded-asset smoke test, and desktop/mobile responsive review in the live preview.
- Release gates: `scripts/verify.sh` plus a clean generated-code diff and race-enabled accounting/storage/server test run.

## Incremental price-provenance evidence — 2026-09-24

- Request details now return and display each recorded/restated price version's nullable cache-read rate and web-search call fee alongside input/output rates. Explicit zero stays distinct from an unknown rate, so the displayed price terms can explain search and cached-token charges.
- The accounting package tests, request-detail component test, dashboard typecheck, management OpenAPI validation, and diff whitespace check passed locally. No provider call or GitHub workflow was used.
