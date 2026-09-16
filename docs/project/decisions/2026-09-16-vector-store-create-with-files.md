# 2026-09-16 — Atomic Vector Store creation with Files

ID: ADR-044 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-039 and ADR-043

**Context.** The OpenAI Vector Store create method accepts optional `file_ids` and one shared chunking strategy. The gateway already has same-key File attachments and the atomic validation path introduced for file batches, so forcing clients into a second request no longer serves a technical constraint. [OpenAI Vector Store create reference](https://developers.openai.com/api/reference/typescript/resources/vector_stores/methods/create)

**Decision.** Accept zero to 2,000 unique same-key `file_ids` during Vector Store creation and support the existing auto-chunking contract when the list is non-empty. Insert the store and every attachment in one transaction through the shared ownership, expiry, duplicate, and retained-capacity checks. Roll back the store and all attachments if any File fails validation.

**Consequences.** The pinned OpenAI SDK can create a ready Vector Store with Files in one call, and the returned usage/count aggregate is immediately accurate. Empty `file_ids` remains valid without chunking. Static token chunking, binary parsing, and semantic indexing remain separate work.
