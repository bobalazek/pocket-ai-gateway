# Gateway-owned OpenAI Image Edit Batches

ID: ADR-031 · Status: accepted · Source: delegated technical choice extending ADR-024, ADR-027, ADR-028, ADR-029, and ADR-030

**Context.** OpenAI Batch accepts `/v1/images/edits` with JSON image references, and the gateway already has a bounded native GPT Image edit path. Provider `file_id` references need binary File ownership, retrieval, and provider affinity that the current Batch-only Files contract does not provide.

**Decision.** Add `/v1/images/edits` to the local OpenAI Batch queue. Creation requires `batches:manage` and `images:edit`. Each item accepts 1–16 `images` plus an optional `mask` as HTTPS URLs or bounded PNG/JPEG/WebP base64 data URLs, and reuses native image routing, admission, accounting, and prices. Provider `file_id` references, streaming, and partial images are rejected. The output line preserves the standard Images response without adding a `model` field; the Batch resource records the public model alias. Aggregate usage is available only when terminal Batch usage is complete; otherwise it is null.

**Consequences.** The existing one-to-four-item, 16 MiB, 24-hour processing, encrypted-result, and 30-day retention bounds apply. Image edits do not fall back after dispatch. The gateway does not claim OpenAI's native Batch discount, rate pool, or larger ceilings. Binary/provider File references, video, legacy Completions, and provider-owned Batch execution remain separate contracts.
