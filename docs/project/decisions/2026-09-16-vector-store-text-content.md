# 2026-09-16 — Gateway-owned Vector Store text content

ID: ADR-041 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-039

**Context.** The official OpenAI Vector Store File API exposes parsed content after attachment. Gateway Files already retain encrypted bytes, so storing a second plaintext or encrypted copy would add retention and backup cost before semantic indexing exists. [OpenAI Vector Store File content reference](https://developers.openai.com/api/reference/typescript/resources/vector_stores/subresources/files/methods/content)

**Decision.** Implement `GET /vector_stores/{vector_store_id}/files/{file_id}/content` for unexpired same-key attachments. Decrypt the source File through the existing serialized transfer boundary, accept valid UTF-8 text without NUL bytes, remove an optional UTF-8 byte-order mark, and return lossless chunks of at most 64 KiB in the official page shape. Derive chunks on request and send `Cache-Control: no-store`; do not persist duplicate plaintext. Preserve the attachment's `other` chunking strategy because these chunks are byte-bounded rather than token-indexed.

**Consequences.** The pinned OpenAI SDK can iterate parsed text content with existing `vector_stores:manage` authorization and key isolation. Binary document parsing, static token chunking, semantic embeddings/search, query rewriting, filters, and Responses `file_search` remain separate contracts and must not be implied by a completed attachment.
