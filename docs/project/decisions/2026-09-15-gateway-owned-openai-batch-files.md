# 2026-09-15 — Gateway-owned OpenAI batch files

ID: ADR-023 · Status: accepted · Source: user decision

**Context.** OpenAI's Files API is the prerequisite resource contract for later Batch input and output. Forwarding provider-owned files would expose provider credentials and couple public IDs, ownership, retention, and deletion to one upstream. The full Files, Uploads, and Vector Stores surface also carries much larger quotas and lifecycle requirements than this local gateway needs.

**Decision.** Implement the five stable OpenAI Files operations under `/api/openai/v1/files`: create, list, retrieve, return content, and delete. A file is gateway-owned by its creating inference key and all operations require `files:manage`. Upload accepts exactly one non-empty `.jsonl` file with `purpose=batch`, limits the file to 16 MiB and the multipart request to 17 MiB, and returns a processed File object. Optional `expires_after` accepts only `anchor=created_at` and 3,600–2,592,000 seconds; omission uses the 30-day maximum.

List uses `after` keyset pagination, 1–100 results per page with a default of 20, ascending or descending creation order with descending as the default, and `batch` or `batch_output` purpose filters. Content returns the exact stored bytes. Expired, deleted, missing, and foreign-key files have the same not-found boundary. Local generated Batch output files may later use `purpose=batch_output`, but this decision does not implement the OpenAI Batches API.

**Consequences.** The system store contains encrypted bounded file bytes and metadata until expiry or deletion, so backup and cleanup behavior includes these resources. The official OpenAI SDK acceptance suite covers upload with expiration, processing wait, retrieve, filters, order, automatic pagination, exact content, deletion, and key isolation. General-purpose uploads, provider-owned files, fine-tuning files, assistants/user-data purposes, vector stores, and OpenAI Batches remain separate contracts.
