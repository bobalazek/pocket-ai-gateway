# Phase 4 — First native gateway

Status: planned; no implementation evidence yet. [Plan index](README.md)

Depends on phases 2–3. Implement the real path end to end through shared admission.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| G4.1 Connections/catalog | Encrypted secrets and external references, egress validation, generic/OpenAI/Anthropic/native Gemini connection configuration, private upstream catalog, stable public model + fixed target | PROV-01, MODEL-01, SEC-01 |
| G4.2 Native protocols | Separate OpenAI, Anthropic, and Gemini compatibility features/routes; native generation/counting/embeddings as supported, SSE/tools, safe errors, cancellation/timeouts; no cross-protocol mapping yet | API-01, API-02, API-03 |
| G4.3 Gateway lifecycle | Authenticate → validate → select → reserve → dispatch → settle; metadata per attempt; errors distinguish rejection/unknown/cancelled | DATA-01, COST-01, ROUTE-01 |
| G4.4 First operator journey | Management APIs, Providers/Models/Requests, scoped playground, copied client configuration; browser never obtains stored provider secrets | UI-01, RUN-01 |

**Exit gate:** a scoped key reaches deterministic native upstreams, records usage, and observes limits; disabled/disallowed connections never dispatch; embeddings preserve index/contract; disconnect cancels work; metadata survives restart.

Optional live smoke uses configured credentials and an explicit small spending ceiling. Passing a mock is not described as real-provider certification.

**Blast radius:** external network/credential boundary, stream delivery, and accounting. This remains an internal development milestone until phase 5 acceptance.
