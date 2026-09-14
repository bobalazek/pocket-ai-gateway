# 2026-09-14 — API namespaces and feature ownership

ID: ADR-009 · Status: accepted · Source: owner's route and auth clarifications

**Context.** Shared model/inference paths with header-selected dialects are confusing. The owner wants /api/anthropic-style roots, distinct compatibility features, global gateway resources, and explicit login/auth ownership.

**Decision.** Protocol roots are /api/openai, /api/anthropic, and /api/gemini; preserve each protocol's version below its root:

- /api/openai/v1/chat/completions and /api/openai/v1/responses
- /api/anthropic/v1/messages
- /api/gemini/v1beta/models/{model}:generateContent

The gateway's own resources live under /api/v1, including /models, /providers, /connections, /keys, /requests, and /usage. Authentication is its own feature at /api/v1/auth; privileged instance/user operations are under /api/v1/admin. The dashboard stays at /_.

**Consequences.** Paths determine client wire formats, not upstream selection. No header sniffing or default ambiguous global inference aliases. Backend and frontend code are feature-oriented. Auth owns setup/login/logout/activation/sessions/recovery; users owns account/role lifecycle; keys owns inference credentials. Session, management-token, and inference-key permissions stay separate.
