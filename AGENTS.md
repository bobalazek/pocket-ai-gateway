# Pocket AI Gateway

Read [docs/README.md](docs/README.md), the current [decisions](docs/project/decisions/README.md), and the relevant [phase](docs/plan/README.md) before implementation. The repository currently contains planning scaffolding; never describe planned capabilities as implemented.

- Keep the single Go runtime and recommended static Next.js export distinct from any future SSR deployment.
- Organize code by feature. Auth, users, keys, OpenAI compatibility, Anthropic compatibility, and Gemini compatibility have separate owners.
- Protocol roots are /api/openai, /api/anthropic, and /api/gemini. Gateway resources/auth/admin are under /api/v1. Paths choose wire format; routes choose upstream providers.
- All inference uses shared authorization, limits, routing, and accounting. Browser sessions/management tokens and inference keys are not interchangeable.
- Default to two local stores. Authoritative admission/usage is atomic in the system store; rich history is an idempotent projection. Remote libSQL/Turso is explicit and tested, never a stale fallback.
- Never log secrets; raw content capture is opt-in. No public telemetry.
- Record decisions as dated entries with context, decision, and consequences; update the plan with real verification evidence.

<!-- CODEGRAPH_START -->
## CodeGraph

In repositories indexed by CodeGraph (a .codegraph/ directory exists at the repo root), use CodeGraph before grep/find or reading code to understand or locate it. Use the codegraph_explore MCP tool when available, or codegraph explore "<symbol names or question>". If there is no .codegraph/ directory, skip it; indexing is the user's decision.
<!-- CODEGRAPH_END -->
