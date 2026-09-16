# Gateway-owned OpenAI Image Generation Batches

ID: ADR-030 · Status: accepted · Source: delegated technical choice extending ADR-024, ADR-027, ADR-028, and ADR-029

**Context.** OpenAI Batch accepts `/v1/images/generations`, and the gateway already has a bounded native image-generation path. Provider Batch execution would add a separate trust, pricing, retention, and reconciliation contract.

**Decision.** Add `/v1/images/generations` to the local OpenAI Batch queue. Creation requires `batches:manage` and `images:generate`; each item reuses direct image validation, native routing, limits, accounting, and prices. Streaming and partial images remain unsupported. The output line preserves the standard Images response without adding a `model` field; the Batch resource records the public model alias. Aggregate usage is available only when terminal Batch usage is complete; otherwise it is null.

**Consequences.** The existing one-to-four-item, 16 MiB, 24-hour processing, encrypted-result, and 30-day retention bounds apply. Image requests do not fall back after dispatch. The gateway does not claim OpenAI's native Batch discount, rate pool, or larger ceilings. Image edits, video, Completions, and provider-owned Batch execution remain separate contracts.
