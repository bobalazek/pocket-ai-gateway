# 2026-09-16 — Gateway-owned Vector Store file batches

ID: ADR-043 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-039

**Context.** OpenAI clients expose one operation for attaching many Files and helpers that poll, retrieve, cancel, and list a batch's files. Gateway Files and Vector Stores are local same-key resources, and their current attachment work is synchronous, so inventing a background ingestion state would add failure modes without doing useful work. [OpenAI File Batches reference](https://developers.openai.com/api/reference/typescript/resources/vector_stores/subresources/file_batches)

**Decision.** Accept either 1–2,000 unique `file_ids` with shared attributes and auto chunking, or per-file objects with their own values. Validate every entry and attach all Files in one database transaction through the existing same-key, expiry, duplicate, and retained-capacity checks. Persist the batch under the Vector Store, report it as immediately `completed`, preserve its terminal counts, and expose retrieve, idempotent cancel, and validated file-list pagination through the official paths. Any invalid, missing, foreign, expired, already-attached, or over-capacity File rolls back the whole batch.

**Consequences.** The pinned OpenAI SDK's `createAndPoll`, `retrieve`, `cancel`, and `listFiles` methods work without provider dispatch or a worker. Cancellation returns the already-terminal batch because local attachment has no observable in-progress state. Detaching or deleting a source File removes it from subsequent batch-file listings while the batch keeps its historical completion count. Static token chunking, binary parsing, embeddings, and semantic indexing remain separate work.
