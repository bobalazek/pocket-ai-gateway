# Decisions

One dated file per material product/architecture choice, following the personal SaaS template. Records are append-only once accepted. A reversal gets a new linked record; update this index to show the current ruling.

Each record states status/source, context, decision, and consequences. A recommended implementation detail is not presented as an explicit user decision.

| ID | Current decision | Status |
| --- | --- | --- |
| ADR-001 | [Product, MIT, users, and deployment](2026-09-14-product-and-runtime.md) | Accepted user decisions; owner/member details are implementation defaults |
| ADR-002 | [Next.js dashboard and Go API boundary](2026-09-14-dashboard-runtime.md) | Accepted |
| ADR-003 | [Two databases and optional remote storage](2026-09-14-storage-boundaries.md) | Two stores/local default/remote capability accepted; transaction design selected |
| ADR-004 | [Go database access](2026-09-14-query-layer.md) | Technical choice made under delegated authority |
| ADR-005 | [Key policies, pricing, and usage history](2026-09-14-usage-and-pricing.md) | Configurable controls and delayed pricing accepted; enforcement semantics selected |
| ADR-006 | [Privacy, catalog, and backups](2026-09-14-privacy-catalog-backups.md) | Accepted user decisions; opt-in network/retention behavior specified |
| ADR-007 | [Earlier protocol proposal](2026-09-14-protocol-boundaries.md) | Protocol scope superseded by ADR-008 and ADR-015 |
| ADR-008 | [Three client protocols and translation](2026-09-14-three-protocol-compatibility.md) | Accepted |
| ADR-009 | [Feature namespaces and authentication](2026-09-14-feature-api-namespaces.md) | Accepted |
| ADR-010 | [Earlier dashboard onboarding decision](2026-09-14-dashboard-system-onboarding-tests.md) | Onboarding protection superseded by ADR-017 |
| ADR-011 | [Dashboard palette, icons, and browser API client](2026-09-14-brand-and-browser-client.md) | Accepted |
| ADR-012 | [Concurrency and coordination](2026-09-14-concurrency-and-coordination.md) | Accepted direction |
| ADR-013 | [Session and API-key security](2026-09-14-session-and-key-security.md) | Technical choice made under delegated authority |
| ADR-014 | [v0.1 local SQLite authority](2026-09-15-v01-local-sqlite-authority.md) | Technical scope refining ADR-003; remote certification retained |
| ADR-015 | [Gateway-owned Responses state](2026-09-15-response-resources.md) | Stored and asynchronous responses accepted; ownership contract selected |
| ADR-016 | [Gateway-owned Conversations and attached streams](2026-09-15-conversation-resources.md) | Technical choice extending ADR-015 |
| ADR-017 | [Browser-first owner setup](2026-09-15-browser-first-owner-setup.md) | Accepted user decision superseding ADR-010 onboarding protection |
| ADR-018 | [Gateway-owned stored Chat Completions](2026-09-15-stored-chat-completions.md) | Technical choice extending Phase 8 compatibility |

Historical: [initial proposal](2026-09-14-initial-proposal.md), superseded by the records above where they differ. Its A/P/D/M identifiers are historical references only; current documents use ADR IDs.

## New-entry template

~~~markdown
# YYYY-MM-DD — Decision title

ID: ADR-NNN · Status: accepted/proposed/superseded · Source: user decision or delegated technical choice

**Context.** What forced the choice and which alternatives mattered.

**Decision.** The selected behavior and scope.

**Consequences.** Implementation obligations, limitations, and what this supersedes.
~~~
