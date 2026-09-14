# Phase 3 — Admission, buckets, quotas, and spend accounting

Status: planned; no implementation evidence yet. [Plan index](README.md)

Depends on phase 2 identity and phase 1 transactional storage. Use deterministic mock attempts; no paid provider is required.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| C3.1 Policy engine | Per-key fixed-window or token-bucket rates, hour/day/week/month/lifetime quotas, global/user/key/connection policies, body/output/batch/concurrency ceilings; all constraints intersect atomically | LIMIT-01, KEY-01 |
| C3.2 Attempt accounting | Requests/attempts/leases/reservations/ledger, price snapshots, decimal-safe estimates, idempotent settlement; each fallback attempt gets its own reservation | COST-01, DATA-01 |
| C3.3 Recovery/clock | Restart interruption classification, unknown usage queue, protected consumption on rotation/import/pruning, clock/period boundary semantics | LIMIT-01, COST-01, OPS-01 |
| C3.4 Policy API/UI | Preview effective limits, scoped usage summaries, price provenance, safe limit edits and audited adjustments | UI-01, COST-01 |
| C3.5 Historical pricing | N/A usage capture, effective-dated prices, preview/reprice jobs, idempotent ledger deltas, original/restated costs and original-period updates | COST-02, DATA-01 |
| C3.6 Store projection | Bounded transactional system outbox to data-store events; idempotent replay, visible lag/full behavior, no accounting loss | DATA-02 |

Frontend: Keys/User policy tabs, usage known/estimated/unknown indicators, limit-denial explanations. Demonstrate with synthetic data clearly labelled as test data.

Dashboard charts use the source-owned shadcn Chart component with Recharts v3, accessible chart layers, tabular summaries, and non-color-only legends. Do not add ApexCharts as a second chart stack.

**Exit gate:** concurrent callers cannot over-admit; denial changes no sibling policy; repeated settlement charges once; month/week transitions and restart preserve allowance; unknown billable work remains reserved; lowering a cap blocks new admissions.

**Blast radius:** costs and access for every future adapter. This phase is a hard dependency of live dispatch, not post-launch hardening.
