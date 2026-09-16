# 2026-09-16 — Gateway-owned OpenAI Moderations Batches

ID: ADR-029 · Status: accepted · Source: delegated technical choice

**Context.** ADR-024, ADR-027, and ADR-028 established one bounded, gateway-owned OpenAI Batch lifecycle for Responses, Chat Completions, and Embeddings. The official Batch API also accepts `/v1/moderations`, and the gateway already has the native moderation route, validation, authorization, encrypted Files, and durable Batch machinery needed to execute those items without another queue.

**Decision.** Extend the existing Batch resource to accept `/v1/moderations`. Creation requires `batches:manage` plus `moderations:classify`; every JSONL line must use `POST`, the selected endpoint, one shared public moderation model, and input accepted by the ordinary Moderations validator. Workers persist the endpoint, recheck the creating key's current grants, and execute through the ordinary native moderation route without post-dispatch fallback. Successful output lines preserve the native moderation response and taxonomy. Batch aggregate usage remains null because the moderation response has no usage object.

**Consequences.** Responses, Chat Completions, Embeddings, and Moderations share the encrypted queue, 1–4-item and 16 MiB input bounds, 24-hour processing deadline, 30-day Batch retention, optional 1-hour through 30-day output retention, cancellation, recovery, and separate success/error Files. Ordinary request-level admission and accounting still apply. The gateway does not claim OpenAI's Batch discount, provider rate pool, or larger limits. Completions, image/video Batch endpoints, and provider-owned Batch execution remain separate contracts.
