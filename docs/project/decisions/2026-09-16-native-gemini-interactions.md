# 2026-09-16 — Bounded native Gemini Interactions

ID: ADR-036 · Status: accepted · Source: delegated technical choice extending the native Gemini contract

**Context.** Google's Interactions API is the current high-level Gemini interface, but its provider storage, agents, tools, background work, and prior-interaction state need distinct ownership and billing contracts.

**Decision.** Expose `POST /api/gemini/v1beta/interactions` first as a synchronous stateless text operation. Require the built-in Gemini preset and explicit public/upstream `interactions` capability, reuse `chat:generate`, force `store:false`, validate completed or incomplete provider responses and usage, normalize the public model alias, and stop after any dispatched semantic response failure.

**Consequences.** Official Google Gen AI SDK coverage proves the path. Normalized output accounting includes reported output and thinking tokens through `total_tokens - total_input_tokens`; positive tool-use usage is rejected. Streaming, provider state, agents, tools, media, structured output, and background execution remain separate contracts.
