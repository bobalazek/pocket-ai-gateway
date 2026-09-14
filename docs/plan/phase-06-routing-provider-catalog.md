# Phase 6 — Routing strategies and provider catalog

Status: planned; no implementation evidence yet. [Plan index](README.md)

Depends on real attempt timing, cost provenance, and compatibility eligibility.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| R6.1 Safe fallback | Ordered primary/fallback, one retry owner, shared deadline, jitter, retryable-rejection rules, live grant checks, attempt-level costs | ROUTE-01, COST-01 |
| R6.2 Strategy evaluation | Weighted, lowest-estimated-cost, and observed-latency routing with freshness/cold-start/exploration/circuits; deterministic known-scenario tests | ROUTE-01, NFR-04, NFR-05 |
| R6.3 Presets/catalog | Certify OpenRouter, Ollama and optional Gemini-compatible preset; native Gemini is already phase 4/5; manual/custom models, bundled/GitHub-refreshed metadata provenance, bounded scheduled data refresh, discovery preview, local overrides, free-only policy | PROV-01, MODEL-01 |
| R6.4 Routing UX | Route preview/rejection reasons, attempt selection explanations, capability badges and price/catalog updates | UI-01, ROUTE-01 |

**Beta gate:** prohibited or semantically incompatible targets never win a strategy; partial output never retries; a failed “fast” target is circuit-excluded; stale/unknown/free prices behave safely; every named v0.1 preset has recorded live or explicitly limited evidence.

Evaluate policy using controlled mock latency/failure/price inputs before live traffic. Latency/cost improvements are reported only if measured; routing does not imply quality equivalence.

**Blast radius:** target choice, data destination, costs, and latency. Provider updates may invalidate earlier capability evidence and require recertification.
