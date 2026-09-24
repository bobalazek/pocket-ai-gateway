# 2026-09-24 — Local reliability signals

ID: ADR-063 · Status: accepted · Source: delegated technical choice implementing the owner's error-rate and alerting request

**Context.** Existing analytics showed failed attempts but summary traffic and the disposable example had no final failures, so error rates and operational alerts were invisible. Provider fallback also makes a failed attempt different from a failed gateway request. Public readiness polling must remain cheap and must not expose private usage or backup state.

**Decision.** The Go usage service reports successful requests, failed requests, failed attempts, and error rate in summaries, daily points, and scoped breakdowns. Error rate is failed / (successful + failed); unfinished requests are excluded. The authenticated admin status computes local alerts for unavailable databases or projection capacity, the latest failed backup, and at least 20 completed requests in the past hour with at least 5% final failures. Public `/readyz` keeps its lightweight checks. The dashboard renders the server's counts and alerts; it does not calculate alert thresholds or send notifications.

**Consequences.** Operators can see failures by day and by key/model/provider, inspect failed requests, and poll admin status from their own monitoring. An indexed request-finish lookup includes long-running requests that complete in the recent window. Alerts are current read-time signals rather than durable incidents, acknowledgements, or email/webhook delivery. Live provider availability is not inferred from local readiness. The disposable screenshot fixture uses only loopback mock upstreams and clearly identifies its synthetic data outside the dashboard.
