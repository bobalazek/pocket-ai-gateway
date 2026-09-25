# Pocket AI Gateway

Read [docs/README.md](docs/README.md), the current [decisions](docs/project/decisions/README.md), and the relevant [phase](docs/plan/README.md) before implementation. Never describe planned capabilities as implemented.

- Keep the single Go runtime and recommended static Next.js export distinct from any future SSR deployment.
- Organize code by feature. Auth, users, keys, OpenAI compatibility, Anthropic compatibility, Gemini compatibility, and System One decisions have separate owners.
- Protocol roots are /api/openai, /api/anthropic, /api/gemini, and /api/systemone. Gateway resources/auth/admin are under /api/v1. Paths choose wire format; routes choose upstream providers.
- All inference uses shared authorization, limits, routing, and accounting. Browser sessions/management tokens and inference keys are not interchangeable.
- Default to two local stores. Authoritative admission/usage is atomic in the system store; rich history is an idempotent projection. Remote libSQL/Turso is explicit and tested, never a stale fallback.
- Never log secrets; raw content capture is opt-in. No public telemetry.
- Record decisions as dated entries with context, decision, and consequences; update the plan with real verification evidence.


## Git workflow

Use the typical feature-branch flow: branch from an up-to-date `master`, keep the branch short-lived and focused, then merge it back with `git merge --no-ff` so the merge is recorded. Keep `master` releasable, and never rebase or force-push a shared branch.

Write commit subjects as [Conventional Commits](https://www.conventionalcommits.org/): a lowercase prefix, a colon, then an imperative, present-tense summary under about 72 characters (for example `feat: add Anthropic combined web tools`).

- `feat:` a user-visible capability
- `fix:` a bug fix
- `docs:` documentation, decision records, or plan updates
- `test:` test-only changes
- `refactor:` a behavior-preserving code change
- `chore:` tooling, dependencies, or maintenance
- `merge:` a branch merged into `master`

Keep each commit focused on one change, stage only the intended files, and never commit secrets. Do not commit, amend, or push unless the user asks.

<!-- CODEGRAPH_START -->
## CodeGraph

In repositories indexed by CodeGraph (a .codegraph/ directory exists at the repo root), use CodeGraph before grep/find or reading code to understand or locate it. Use the codegraph_explore MCP tool when available, or codegraph explore "<symbol names or question>". If there is no .codegraph/ directory, skip it; indexing is the user's decision.
<!-- CODEGRAPH_END -->
