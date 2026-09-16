# 2026-09-16 — Gateway-owned OpenAI Vector Store lifecycle

ID: ADR-039 · Status: accepted · Source: user-approved Phase 8 API-parity scope with delegated technical contract

**Context.** OpenAI clients expect Vector Stores to have their own lifecycle, ownership, pagination, metadata, and expiry contract. Gateway Files are local encrypted objects and cannot be treated as indexed provider resources without a separate ingestion and search design.

**Decision.** Implement create, list, retrieve, update, and delete at `/api/openai/v1/vector_stores` as local resources owned by the creating API key and guarded by `vector_stores:manage`. Empty stores are immediately `completed`; metadata uses the standard 16-string-pair limits; optional expiry is anchored at `last_active_at` for 1–365 days; lists use bounded keyset pagination; expired, deleted, missing, and foreign-key IDs share the not-found boundary. Vector Stores share the existing retained-resource count and byte ceilings. Non-empty `file_ids` and every `chunking_strategy` return `unsupported_feature` until ingestion exists.

**Consequences.** The five official SDK lifecycle methods work without provider storage or dispatch. File attachment, file batches, parsed content, semantic search, and Responses `file_search` remain pending and cannot be inferred from this metadata-only lifecycle.
