# 2026-09-16 — Backend-owned dashboard metadata

ID: ADR-034 · Status: accepted · Source: owner's explicit direction

**Context.** Provider and model support rules were duplicated as adapter and capability conditions in dashboard components. That made UI text stale when backend compatibility changed and let the browser imply behavior that the gateway did not enforce.

**Decision.** The Go management API owns provider capabilities, preset operations, adapter labels, and routing constraints. The dashboard renders that metadata and does not infer provider or model policy from adapter names, model IDs, or capability combinations. Advanced free-form diagnostics remain free-form until the backend can publish their exact eligible values through the same execution policy.

**Consequences.** Provider and model cards stay visual. New support rules are implemented and tested once in Go, then exposed as metadata when the dashboard needs them. This extends ADR-002 and ADR-011 without changing the Next.js static-export or browser-client boundaries.
