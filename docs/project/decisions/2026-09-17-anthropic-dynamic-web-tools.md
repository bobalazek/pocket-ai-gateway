# 2026-09-17 — Anthropic dynamic web filtering

ID: ADR-054 · Status: accepted · Source: delegated technical choice extending the user-approved API compatibility scope

**Context.** Anthropic's `web_search_20260209` and `web_fetch_20260209` add provider-managed dynamic filtering through code execution. The existing gateway already had bounded native basic web search and fetch, but rejected these versioned tools and treated nonzero code-execution usage as unknown.

**Decision.** Accept one `web_search_20260209` or `web_fetch_20260209` tool anywhere its basic predecessor is accepted. Keep the gateway's required 1–4 `max_uses` ceiling and every existing scope, routing, combination, and no-fallback rule. The basic versions default to direct invocation; the dynamic versions default to `code_execution_20260120`. `allowed_callers` may explicitly select direct, `code_execution_20250825`, `code_execution_20260120`, `code_execution_20260521`, or a unique combination, and `strict` must be boolean when present. Preserve native caller/result shapes and validate nonnegative `code_execution_requests` only when a code-execution caller is available. Dynamic invocation additionally requires `web_search_dynamic` or `web_fetch_dynamic` on both the public and upstream model, preventing routing to models that only support direct hosted-tool calls.

**Consequences.** Dynamic filtering needs no embedded execution runtime because Anthropic provisions it. Anthropic documents no separate code-execution charge when these web tools are present, so token and search/fetch accounting remain on the existing path. `web_fetch_20260309` cache bypass, the 20260318 response-inclusion versions, standalone code execution, computer/browser use, and connectors remain separate contracts.
