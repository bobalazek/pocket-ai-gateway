# 2026-09-15 — Gateway-owned stored Chat Completions

ID: ADR-018 · Status: accepted · Source: delegated technical choice extending the provider API expansion

**Context.** OpenAI Chat Completions exposes stored-object list, retrieve, metadata update, delete, and message-list operations. Passing `store:true` to an upstream would split ownership, retention, and authorization across providers and make cross-provider routes inconsistent.

**Decision.** Pocket AI Gateway owns stored Chat Completions locally. `store:true` is limited to buffered JSON generation, becomes `store:false` before native dispatch, and is omitted by translated requests. The gateway durably settles provider usage before atomically publishing the resource and finalizing the logical request. It replaces the provider ID and model with its key-owned ID and public model, stores the original messages and bounded metadata for 30 days, and exposes the official lifecycle paths. Omitted, null, or false `store` remains stateless. Stored Chat Completions and stored Responses share retention count and byte ceilings. Lists use bounded SQL keyset pages with normalized metadata predicates.

**Consequences.** The creating inference key is required for every lifecycle operation, including after key rotation; foreign, expired, and missing objects have the same 404 response. Provider usage remains authoritative when local persistence fails after a successful dispatch, while the logical request is finalized as failed. Failed-request cleanup retries immediately, and periodic usage maintenance repairs old orphaned requests and releases their request-level concurrency leases. Stored streaming remains unsupported until incremental durable semantics are designed and tested.
