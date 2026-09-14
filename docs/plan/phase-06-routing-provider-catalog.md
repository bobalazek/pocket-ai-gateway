# Phase 6 — Routing strategies and provider catalog

Status: complete. [Plan index](README.md)

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

## Evidence — 2026-09-15

- One routed gateway path now owns target eligibility, deterministic strategy ordering, live admission checks, pre-response retry, per-attempt settlement, shared deadlines, jitter, observations, and circuit exclusion across OpenAI, Anthropic, Responses, and Gemini entry points.
- Public models store versioned route targets, weights, priorities, free-only policy, selection reasons, and safe rejection metadata. Fixed and embedding routes accept one enabled target; free-only prices expire after 24 hours and are atomically rechecked. Deterministic tests cover fallback, cost, latency, weighted/fixed ordering, free-only exclusion/freshness, circuit opening, and request history.
- OpenAI, Anthropic, Gemini, OpenRouter, and credential-free local Ollama presets are available. Operator-triggered or opt-in scheduled GitHub catalog refresh accepts at most 1 MiB and 5,000 validated metadata entries and never publishes or grants them.
- The dashboard exposes presets, route editing, input-aware route preview, catalog source/schedule/status, bounded candidate details and provenance, capability context, and request selection explanations through the typed API client.

### Preset certification record

| Preset | Evidence | Limit recorded for this release |
| --- | --- | --- |
| OpenAI | Native and translated request/response/error/stream contract tests against a controlled OpenAI-shaped server | No live OpenAI account call; recertify against the provider before beta release |
| Anthropic | Native and translated Messages contract tests, header checks, token accounting, and stream fixtures | No live Anthropic account call; Anthropic cross-provider streaming remains intentionally unavailable |
| Gemini | Native and translated generate, stream, count-token, embedding, error, and model fixtures | No live Google account call; opaque thought signatures are rejected |
| OpenRouter | Preset validation plus OpenAI-compatible routing and credential tests | No live OpenRouter account call; provider-specific extensions are outside the certified subset |
| Ollama | Credential-free preset, private-network URL, and OpenAI-compatible route validation | No daemon is bundled and no live local model was used; availability depends on the operator's Ollama endpoint |

These are explicit limited certifications permitted by the beta gate. Live-provider certification remains a release task and no compatibility guarantee is inferred beyond the tested contract fixtures.
