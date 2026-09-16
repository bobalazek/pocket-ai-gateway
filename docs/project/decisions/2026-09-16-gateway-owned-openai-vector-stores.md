# 2026-09-16 — Gateway-owned OpenAI Vector Store lifecycle

ID: ADR-039 · Status: accepted · Source: user-approved Phase 8 API-parity scope with delegated technical contract

**Context.** OpenAI clients expect Vector Stores and their File attachments to have lifecycle, ownership, pagination, metadata, and expiry contracts. Gateway Files are local encrypted objects and cannot be treated as indexed provider resources without a separate parsing and search design.

**Decision.** Implement create, list, retrieve, update, and delete at `/api/openai/v1/vector_stores` as local resources owned by the creating API key and guarded by `vector_stores:manage`. Implement the matching attach, list, retrieve, update, and detach File subresource for unexpired same-key gateway Files. Stores and attachments are immediately `completed`; attachment attributes accept 16 bounded scalar values; aggregate usage reports active source-file bytes; optional expiry is anchored at `last_active_at` for 1–365 days; lists use bounded keyset pagination; expired, deleted, missing, and foreign-key IDs share the not-found boundary. Store and attachment metadata share retained-resource ceilings. Auto chunking is accepted but reported as `other` because no parsed chunks exist.

**Consequences.** Ten official SDK lifecycle methods work without provider storage or dispatch. Detaching preserves the encrypted source File; deleting or expiring that File removes the attachment. Create-with-`file_ids`, static chunking, file batches, parsed content, semantic search, and Responses `file_search` remain pending.
