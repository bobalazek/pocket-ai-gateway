# 2026-09-14 — Native forwarding and translation

ID: ADR-007 · Status: recommended implementation; asynchronous extension pending clarification

**Context.** The owner asked what native forwarding/translation means and requested synchronous/asynchronous behavior.

**Decision.** Native forwarding preserves a supported API dialect on both sides, such as Anthropic Messages client → Anthropic provider. Translation maps OpenAI Chat requests/responses/events to Anthropic Messages, or the reverse, for the tested shared subset. Use both paths through identical key, limit, routing, and accounting enforcement.

Ordinary HTTP responses and SSE streaming are initial scope. Streaming is incremental delivery, not a durable background job. Job-ID submission/polling/cancellation is a distinct proposed extension until clarified; native provider Responses background/state features remain rejected in the stateless subset.

**Consequences.** No silent loss of tool/reasoning semantics and no claimed universal compatibility. The request/attempt chain explains fallback and spending. Tool calls are forwarded for the caller to execute; the gateway does not execute them.
