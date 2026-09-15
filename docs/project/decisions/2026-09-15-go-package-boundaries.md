# 2026-09-15 — Go package boundaries

ID: ADR-022 · Status: accepted

**Context.** The compatibility gateway grew to include HTTP orchestration, durable resources, cross-protocol codecs, provider dispatch, and background work in one package. File count is not itself a Go design problem, but protocol conversion has no reason to depend on gateway state.

**Decision.** Keep the request lifecycle in `internal/gateway`. Move pure OpenAI, Anthropic, Gemini, and Responses conversion into the leaf `internal/protocol` package. Keep provider connection configuration and route selection in `internal/features/providers`; a preset selects a wire protocol and does not become a second adapter hierarchy.

Create narrower resource packages only when they can own behavior without exporting gateway internals or adding one-implementation interfaces. Split large files by cohesive behavior as they change; do not reorganize tests or files solely to meet a line-count target.

**Consequences.** Gateway orchestration can depend on protocol conversion, identity, provider routing, and usage accounting while protocol conversion remains independently testable. Future Files and Batches work starts from these boundaries instead of expanding the previous catch-all feature package.
