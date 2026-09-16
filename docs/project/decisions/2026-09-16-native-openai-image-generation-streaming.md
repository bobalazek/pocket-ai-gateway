# 2026-09-16 — Native OpenAI image-generation streaming

ID: ADR-035 · Status: accepted · Source: delegated technical choice implementing Phase 8 parity

**Context.** OpenAI GPT Image generation can return named partial-image and completed SSE events. Passing arbitrary media streams through would make completion, memory, usage, and fallback behavior ambiguous.

**Decision.** Support direct `POST /api/openai/v1/images/generations` streaming only through the built-in OpenAI preset and GPT Image upstream model IDs. Require `n=1`, accept `partial_images` from 0 through 3, validate and forward complete named events incrementally with a 16 MiB per-event limit, require exactly one terminal completed event, and settle from its provider usage without retaining image bytes. A dispatched image request never falls back. Gateway-owned Batches remain non-streaming, and image-edit streaming remains a separate contract.

**Consequences.** Official OpenAI SDK consumers can iterate image-generation events while the gateway keeps bounded memory and deterministic accounting. Compatible endpoints and other image operations need explicit certification before receiving the same streaming behavior.
