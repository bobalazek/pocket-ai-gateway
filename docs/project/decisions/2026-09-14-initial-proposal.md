# Historical initial proposal — superseded

This is the initial recommendation before the owner clarified the choices. It is retained as history, not an implementation contract. The [current decision index](README.md) records the later user decisions, including MIT, Next.js, and two databases.

# Decision register

Status meanings: **confirmed** comes from the user's request; **recommended** is the working default for this plan; **needs decision** requires a maintainer choice before the affected release; **needs spike** requires implementation evidence. Recommendations are not represented as user approvals.

| ID | Question and options | Ruling and reason | Status |
| --- | --- | --- | --- |
| L1 | Runtime: single process or infrastructure stack? | One executable, one server, one local data directory; optional Docker. End users do not assemble services. | Confirmed |
| L2 | Product edition and ownership? | One open-source self-hosted edition, one instance shared by local users. No hosted billing/tenant hierarchy. | Recommended |
| P1 | One administrator or users? | Owner, admin, and member accounts from the first usable alpha. This supersedes the source PRD's single-admin restriction. Exact role split remains a reversible planning assumption. | Users confirmed; roles recommended |
| P2 | What is the product name? | Pocket AI Gateway; executable pocket-ai-gateway; default data folder pocket_gateway_data. Working name, consistent with the repository. | Recommended |
| P3 | License: Apache-2.0, MIT, or AGPL? | Recommend Apache-2.0. Do not claim a license grant or add legal boilerplate on the owner's behalf before selection. | Needs decision before public release |
| A1 | Backend: Go, JavaScript server, or embed PocketBase? | Standalone Go modular monolith using net/http. Borrow PocketBase's deployment experience; avoid coupling inference policies to a general BaaS API. | Recommended |
| A2 | SQLite, Turso/libSQL, or remote DB? | Local SQLite with WAL through database/sql and modernc.org/sqlite. Verify the driver's bundled engine, supported platforms, locking, and snapshot behavior in phase 1. No remote DB dependency. | Recommended; driver needs spike |
| A3 | One DB, two DBs, or two generic tables? | One gateway.db, separate domain tables for configuration, identity, accounting, and history. Keep admission and accounting atomic. Split disposable history into another DB only after measured write contention warrants it. | Recommended |
| A4 | Next.js or static dashboard? | React + strict TypeScript + Vite, embedded static assets through go:embed. Browser routing and same-origin Go APIs. Next.js static export is possible, but SSR is unnecessary for this dashboard. | Recommended |
| A5 | ORM, sqlc, or direct SQL? | Start with explicit parameterized SQL and database/sql, versioned SQL migrations, and Go structs. Add sqlc only when query scanning becomes repeated maintenance work. No generic repository layer. | Recommended |
| A6 | Dependency policy? | Standard library for HTTP, JSON, logs, CLI flags, embedding, time, and crypto primitives. Use maintained dependencies where needed for SQLite, password hashing, sessions, archive encryption, locking, and UI accessibility; verify them when introduced. | Recommended |
| D1 | Native forwarding or translation everywhere? | Native paths preserve supported native semantics. Translate only a tested shared chat/tool subset. Unknown semantics fail before dispatch; never silently drop them. | Recommended |
| D2 | Rate windows or buckets? | Persistent token buckets for short-term request/token flow; separate hour/day/week/month/lifetime quotas and spending caps. All applicable instance/user/key/connection constraints intersect. | Confirmed capability; algorithm recommended |
| D3 | Routing scope? | Fixed, ordered fallback, weighted, lowest estimated cost, and lowest observed latency. Adaptive strategies ship only after accounting and route measurements exist. No speculative parallel racing. | Confirmed capability; sequencing recommended |
| D4 | Catalog/free models? | Manual registration, provider discovery, curated versioned presets, and explicitly priced free models. Publication and policy changes require operator action; no automatic switch to paid models. | Confirmed capability; safeguards recommended |
| D5 | Backup/update experience? | Early offline snapshots; encrypted portable backup and verified restore, scheduled local backup, safe manual binary replacement for v0.1. Optional authenticated self-update in the next release after platform/signature work. | Backup/upgrade capability confirmed; sequence recommended |
| D6 | Remote provider choice? | OpenAI, Anthropic, Gemini compatible, OpenRouter, generic compatible, and Ollama in v0.1. Additional major provider certifications and native cloud adapters in phase 8. | Recommended rollout |
| M1 | Privacy and telemetry? | Request metadata by default; prompt/response/tool argument capture off. No product telemetry or surprise outbound catalog/pricing/update calls. | Recommended |
| M2 | Spend guarantee? | Durable conservative reservations enforce a gateway estimate. Unknown billable outcomes retain reservations. Provider invoices remain external and may differ. | Accepted limitation |

## Resolved contradictions with the supplied PRD

| Source position | Updated ruling |
| --- | --- |
| Sole administrator, no multi-user administration | P1: multiple local users; explicit account ownership and scoped member views |
| Requests/minute and monthly spend only | D2: token buckets plus hourly, daily, weekly, monthly, and lifetime controls |
| Latency/cost routing left outside initial stable scope | D3: phase 6, after instrumentation and safety gates |
| Configuration might be separate DBs or generic tables | A3: one database, distinct tables; WAL sidecars are expected |
| Cobra/sqlc/TanStack Query included immediately | A6/A5: introduce only where the implemented surface needs them |
| Model list treated as a bundled static truth | D4: source/version/verification metadata with explicit updates and overrides |

## Readiness and revisit triggers

| Area | Readiness / next evidence |
| --- | --- |
| Existing repository | Confirmed empty: no application files, dependencies, commits, or CodeGraph index at planning time |
| Product and deployment boundaries | Defined in this register and PRD |
| Identity and permissions | Defined under the recommended owner/admin/member model; revise if the owner chooses a different split |
| Persistence and accounting | Logical model defined; schema/driver crash and contention tests required before real dispatch |
| Protocol translation | Boundaries defined; golden fixtures and official SDK runs required in phases 4–5 |
| Backup and platform behavior | Driver snapshot, OS lock, crash recovery, and Windows replacement behavior require probes |
| UI | Structural contract defined; visual tokens and keyboard/mobile walkthrough required before screen implementation |
| Legal/release | License and a real security-reporting channel require maintainer selection |

Revisit the single-process architecture only if measured workloads miss the documented targets after bounded queries, retention, and transaction tuning. Reconsider the product if a scoped gateway cannot preserve stream/tool semantics or enforce admission without adding mandatory external services. Do not weaken security or silently change the single-executable promise to meet a benchmark.
