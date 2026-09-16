# 2026-09-15 — Anthropic streams use the public model alias

ID: ADR-025 · Status: accepted · Source: delegated technical choice extending ADR-020

**Context.** Native Anthropic SSE previously exposed the selected upstream model in `message_start.message.model`, while clients address the gateway through stable public models. This made a stream depend on provider-specific naming and differed from translated responses.

**Decision.** Pocket AI Gateway rewrites only `message_start.message.model` to the requested public model as each native Anthropic event arrives. Other event types and provider fields pass through unchanged. The gateway bounds each event before buffering and preserves JSON numbers exactly while changing the model field.

**Consequences.** Anthropic SDK consumers receive the same public identity they requested without delaying the full stream. Malformed or oversized `message_start` events fail instead of exposing an unnormalized provider payload. This supersedes only ADR-020's statement that SSE preserves the provider's complete `message_start`; its web-search routing, accounting, and event rules remain accepted.
