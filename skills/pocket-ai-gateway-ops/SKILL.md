---
name: pocket-ai-gateway-ops
description: Inspect a Pocket AI Gateway instance, explain its current health and usage statistics, and guide local snapshot or restore operations. Use for operator questions about a running gateway; do not use for provider API requests or generic AI gateway design.
---

# Pocket AI Gateway operations

Resolve the instance URL from the user or local configuration; otherwise use `http://127.0.0.1:8080`. Read the instance's `/llms.txt` before assuming an endpoint exists.

For a health question, call `GET /healthz` and distinguish process liveness from storage, projection, provider, or authorization readiness. Use authenticated `GET /api/v1/usage` for totals, `/api/v1/usage/unresolved` for uncertain attempts, and `/api/v1/admin/usage/outbox` for projection lag. If a requested statistic has no implemented endpoint, say that plainly instead of estimating it.

When authenticated management APIs are available, use the documented `/api/v1/` endpoints and preserve their scope, period, currency, provenance, and timestamp in the answer. Treat missing prices as unknown rather than free. Separate accepted requests from upstream attempts and provider-reported usage from estimates.

Snapshot and restore are local CLI operations. Inspect `docs/guides/operations.md` before proposing commands. A restore must target an absent directory and must never replace the active data directory in place. Do not expose cookies, API keys, recovery codes, provider credentials, captured content, or database files in output.

Read-only inspection is the default. Run mutations only when the user asks for that specific operation.
