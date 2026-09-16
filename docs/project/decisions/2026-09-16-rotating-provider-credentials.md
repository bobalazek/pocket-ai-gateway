# 2026-09-16 — Rotating provider credential references

ID: ADR-038 · Status: accepted · Source: delegated technical choice

**Context.** Azure OpenAI and Vertex AI can use short-lived OAuth bearer tokens, while Docker and Kubernetes commonly rotate secrets through mounted files. Keeping a token captured at connection-save time would fail after rotation, and adding three cloud identity SDKs would enlarge the single-binary runtime before live cloud certification exists.

**Decision.** Keep encrypted local credentials for stable API keys and extend write-only external references to `env:NAME`, `file:/absolute/path`, `bearer-env:NAME`, and `bearer-file:/absolute/path`. Resolve the reference for every route selection, limit the resolved value to 16 KiB, reject empty or multiline values, and use the bearer variants to override a preset's default authentication header. Mounted files may be atomically replaced by the operator's cloud identity agent without restarting the gateway.

**Consequences.** Azure API keys retain the `api-key` header; Azure Entra and Vertex access tokens use bearer references; Bedrock API keys remain bearer credentials. Pocket AI Gateway does not mint cloud tokens in this slice. The external process or platform must refresh them, and live IAM certification remains open.
