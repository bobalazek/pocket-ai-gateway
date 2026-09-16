# 2026-09-16 — Gateway-owned OpenAI File purposes

ID: ADR-037 · Status: accepted · Source: delegated technical choice extending ADR-023

**Context.** ADR-023 restricted Files to Batch JSONL while the surrounding ownership, encryption, pagination, retention, content, and deletion controls were already general. OpenAI's current input purposes are `assistants`, `batch`, `fine-tune`, `vision`, `user_data`, and `evals`.

**Decision.** Accept those six input purposes on the existing gateway-owned Files lifecycle. Batch, fine-tuning, and evaluation inputs require a `.jsonl` filename. All Files remain encrypted, creating-key-owned, immediately processed, at most 16 MiB, and retained for 1 hour through 30 days. Generated Batch output keeps the internal `batch_output` purpose. Upload resources remain Batch-only.

**Consequences.** A File may be stored and retrieved under an official purpose, but it is not forwarded to a provider and does not make provider fine-tuning, evaluation, Assistants, or Vector Store APIs available. ADR-023's ownership and bounded-retention controls still apply; its Batch-only input-purpose restriction is superseded.
