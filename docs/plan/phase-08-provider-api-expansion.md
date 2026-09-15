# Phase 8 — Broader providers, full API inventory, and optional self-update

Status: in progress. The broader OpenAI-compatible preset catalog and its deterministic metadata checks are implemented; live provider certification and the larger resource/auth work remain open. [Plan index](README.md)

Begins after v0.1 unless a specific provider becomes a prerequisite for the user's first deployment.

| Work package | Completion evidence | Dependency / uncertainty |
| --- | --- | --- |
| Provider certification queue | Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, Perplexity each have current official-doc review and per-feature tests | Account/model availability; compatible adapter reused only where proven |
| Native cloud adapters | Azure OpenAI, Bedrock, and Vertex AI handle their actual auth/resources/errors/streams and reuse all gateway controls | Credentials, IAM, API/resource semantics; separate probes first |
| Explicit self-update | Authenticated release, dry-run, staged correct-platform swap, restart/health check and rollback tested on supported OS/service layouts | Release trust setup, Windows/process replacement |
| Full API parity inventory | Enumerate official OpenAI/Anthropic/Gemini endpoints/fields; implement remaining stateful response/resource ownership, files/batches/caches, hosted tools, multimedia/realtime surfaces with explicit capability matrix | Full compatibility goal; separate per-operation contracts and evidence; never equate unsupported with done |
| Background delivery | Durable job submission, polling, cancellation, result retention, ownership and charging contract if selected by owner | Pending clarification; streaming already required |
| Remote storage certification | Add a maintained libSQL/Turso driver, remote migrations, single-writer lease, unknown-commit handling, paired export/restore, and no stale-local fallback | Real Turso account and destructive disposable-database tests; local SQLite remains v0.1 authority |

No semantic cache, distributed coordinator, plugin marketplace, automatic prompt rewriting, or speculative hosted edition is included. Add those only after an explicit use case justifies their persistence/security cost.
