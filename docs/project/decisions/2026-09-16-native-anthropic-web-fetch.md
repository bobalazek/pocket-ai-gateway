# 2026-09-16 — Bounded native Anthropic basic web fetch

ID: ADR-032 · Status: accepted · Source: delegated technical choice extending the user-approved Phase 8 compatibility scope

**Context.** Anthropic Messages can fetch remote pages and PDFs through a provider-hosted tool. Fetched content consumes model input tokens, native document and citation blocks must remain intact, and retrying after dispatch can duplicate remote access or billable work.

**Decision.** Pocket AI Gateway accepts one `web_fetch_20250910` tool in JSON or SSE `POST /api/anthropic/v1/messages`. `max_uses` is required from 1 through 4 and `max_content_tokens` from 1 through 16,384. The content-token value is an approximate text-extraction limit and does not bound binary PDF input. Up to ten plain hostnames may be allowed or blocked, never both; citations may be enabled or disabled. The public and upstream models must publish `chat` and `web_fetch`, the target must use the Anthropic adapter with the built-in `anthropic` preset, and the key needs `chat:generate` plus `messages:web_fetch`.

The gateway preserves native server-tool use, fetched text or PDF document blocks, citations, embedded errors, JSON/SSE usage, and the public model alias. Token/spend policies, lowest-cost routing, and free-only routing reject fetch requests before dispatch because input cannot be hard-bounded. Provider-reported usage is still settled with the ordinary versioned input/output token price. The gateway does not translate or retry a dispatched fetch. Web search, prompt caching, local Message Batches, later fetch versions, and provider-owned tool extensions are rejected for this slice.

**Consequences.** Operators opt in through provider, upstream-model, public-model, user-grant, and key-scope controls. `usage.server_tool_use.web_fetch_requests` contributes to generic tool activity while tokens and cost remain authoritative through the normal accounting path. Remote content is still untrusted model input, so applications must treat citations as provenance rather than proof of safety.
