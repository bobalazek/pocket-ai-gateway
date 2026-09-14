# 2026-09-14 — Key-centered policies and delayed pricing

ID: ADR-005 · Status: accepted capability; enforcement details selected · Source: owner's follow-up

**Context.** Limits should be configurable per API key, using rate windows or buckets. Models may have unknown prices at request time, with prices becoming known later.

**Decision.** Keys select fixed-window or token-bucket rate policies, plus independent hourly/daily/weekly/monthly/lifetime quotas and spend caps. User/instance/connection ceilings also apply; keys are the primary operational surface. Record immutable usage, model, provider, time, and pricing provenance. Unknown price is N/A, not zero.

Add effective-dated price versions and idempotent historical repricing. Preserve original estimates/unknown status and record priced/restated totals as explicit ledger adjustments. Apply corrections to their original usage periods and lifetime totals, not as invented new requests.

Strict caps require a bounded price estimate before dispatch. Observational mode can accept unknown-priced usage; it clearly lacks a hard monetary admission cap. An explicit provisional price can support conservative reservation. Later repricing may expose an overrun and block new traffic; it cannot undo already incurred spend.

**Consequences.** The dashboard shows as-recorded versus restated cost and unresolved usage. A client request and its fallback attempts form a traceable chain; every potentially billable attempt is reserved/accounted separately. Rotation cannot reset the logical key's history or counters.
