# 2026-09-14 — Three first-class API protocols

ID: ADR-008 · Status: accepted · Source: owner's explicit compatibility clarification

**Context.** The earlier plan focused on OpenAI/Anthropic with Gemini as a compatible upstream preset. The owner requires native OpenAI, Anthropic, and Gemini client APIs and translation to a selected provider.

**Decision.** Treat all three as first-class client and upstream protocols. Test the nine client/provider-family combinations for eligible generation features; OpenAI Chat and Responses have distinct additional cases. Translate request, response, error, usage, tool, and stream semantics. Native Gemini is initial scope, not postponed to cloud-provider expansion.

**Consequences.** Supersedes ADR-007's narrower protocol emphasis and the original native-only Responses proposal. Full fidelity is the goal; each endpoint/field needs an explicit tested/translated/native-only/pending/unsupported capability status. The phase 8 inventory tracks remaining resource/state/multimodal surfaces; an unimplemented endpoint is never called compatible.
