# Phase 5 — Compatibility, translation, and usable alpha

Status: implementation complete; verification evidence recorded below. [Plan index](README.md)

Depends on native transport and durable accounting. Split the XL phase into one direction/operation per implementation task.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| X5.1 Shared text mapping | All nine OpenAI/Anthropic/Gemini family paths, namespace-native model listing/errors, system/stop mappings | API-01 |
| X5.2 Tool/stream mapping | Multiple tool calls, IDs/results, fragmented arguments, typed content events, usage deltas and completion; malformed/truncated output never becomes success | API-01, API-02 |
| X5.3 Responses/features | Native and translated stateless Responses items/functions/events for all three upstream families; image/strict-output/affinity fixtures; state/resource extensions tracked in phase 8 | API-03, API-01 |
| X5.4 SDK matrix/history | Pinned OpenAI, Anthropic, and Google Gen AI SDK tests across all nine family paths plus Responses; assert exact /api/<protocol>/version URLs, capture boundaries, attempt/tool detail, useful unsupported-feature errors | API-01, UI-01, DATA-01 |

**Alpha gate:** phases 1–5 acceptance passes, offline recovery is documented, the core dashboard is usable, and each published claim has a test. It supports users/admins, all three client dialects, tools/streams, scoped keys, durable all-period controls, spending estimates, and basic history.

The alpha explicitly lacks phase 6 strategies/preset certification and phase 7 full release operations. Publish under MIT with the license notice and honest limitations if an alpha is distributed externally.

**Blast radius:** client compatibility and semantic integrity. Refusal, reasoning, tool, and stream failures are release blockers for claimed combinations.

## Verification evidence

- Pinned OpenAI 7.15.0, Anthropic 0.125.0, and Google Gen AI 2.22.0 SDK tests exercise all nine client-family to upstream-family paths, stateless Responses through all three targets, representative translated streams, native error decoding, and exact namespace URLs against a real Go gateway with deterministic providers.
- Go fixtures cover system and multi-turn text, sampling and stop controls, images, JSON schemas, strict tools, tool choice/results, fragmented arguments, safety refusals, malformed/truncated streams, missing usage, target path and safe tool-count history, and unsupported provider-affine features.
- `./scripts/verify.sh` passed on September 15, 2026: formatting, OpenAPI validation, generated-code drift, static analysis, all Go tests, selected race tests, dashboard build/type checks/tests, cross-builds, and copied-binary smoke tests.
- The documented multi-stage non-root Docker image built successfully, and the Compose definition validated with persistent data storage, read-only root filesystem, and local HTTP restricted to an explicit loopback public URL.
