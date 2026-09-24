# 2026-09-24 — Scoped gateway analytics

ID: ADR-062 · Status: accepted · Source: delegated technical choice implementing the owner's analytics request

**Context.** The dashboard needs comparable charts for API keys, users, models, providers, and traffic without duplicating accounting rules in the browser or exposing another user's activity. Retries and fallback can begin on a different day from the original request.

**Decision.** The Go usage service provides one role-scoped breakdown API with bounded UTC time ranges, dimensions, sorts, and pagination. Distinct request counts and gateway duration use request start time; attempt tokens, cost, and failures use attempt start time, consistent with usage summaries. A request touching multiple provider connections appears in each connection's request count. Failed attempts remain distinct from final failed requests. The dashboard shares one time window across charts, defaults to seven days, and drills into server-filtered request history. A local request-start index supports owner-wide reads.

**Consequences.** Chart totals across provider rows may overlap, and request counts need not sum to same-period attempt usage after cross-boundary retries. Known spend reflects recorded or restated prices, not provider invoices; unknown attempts stay visible. The endpoint retains authorization and pagination limits. Large-history latency and complete keyboard accessibility still require measurement before a production-scale claim.
