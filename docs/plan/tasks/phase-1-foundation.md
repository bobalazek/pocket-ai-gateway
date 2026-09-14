# F1.1 — Build the embedded runtime

Phase: 1 · Status: complete · Depends on: planning documents · Blocks: phase 2

Requirements: RUN-01, OPS-02 · Effort: M · Confidence: high

## Context and reading

Pocket AI Gateway must ship as a single executable containing the dashboard. This first task proves that deployment shape and the minimum secure owner bootstrap without introducing inference or provider behavior prematurely.

Read [shared context](00-context.md), [architecture](../../architecture/README.md), RUN-01 in the [PRD](../../project/prd.md), and the shell/state rules in [dashboard](../../design/dashboard.md).

## Build

- Establish the Go module using the verified repository module path and supported current Go toolchain.
- Add cmd/pocket-ai-gateway/main.go and only the needed internal/app and internal/server runtime code.
- Implement serve/version, explicit listen/data-dir options, flags → environment → defaults, safe startup logging, and signal-driven HTTP shutdown.
- Add web/package.json, a pinned lockfile, strict TypeScript/Next.js export configuration, the smallest shell in web/src/app/, and web/embed.go for production assets.
- Serve exported /_/ screen pages and their assets from embedded files. Known screen URLs with query-string record IDs work after refresh; unknown dashboard URLs get the dashboard 404. Unknown /api/, /api/openai/, /api/anthropic/, /api/gemini/, and unprefixed /v1/ paths get JSON 404s.
- Provide minimal /healthz, a safe startup status screen, and a first-run owner claim protected by a short-lived code in an owner-only local file. This task does not accept provider secrets or expose broader administration.
- Add one verification entry point, initially scripts/verify.sh or an equivalently simple portable command, covering the implemented Go/frontend checks and artifact smoke.
- Add repository ignore rules for build artifacts, local data, secrets, and generated frontend output. Update README with actual build/run commands only after they work.

Resolve dependency versions from official registries and record them. Introduce only packages used by this phase's runtime, SQLite, password hashing, and frontend.

## Invariants

Production needs only the executable and its mutable data directory; embedded assets cannot rely on local source/build files. Default binding is loopback. Unknown API paths never return HTML. Startup/shutdown and errors never log secret values. No provider traffic occurs in this task.

Keep version/status output honest: owner bootstrap is implemented; login, broader administration, inference, and managed backup remain unavailable. No fake provider/card data that resembles real usage.

## Acceptance and verification

1. A clean checkout builds the UI then binary through the documented command.
2. Copy the binary to a temporary empty directory with no source/web/out; launch it offline and confirm /_/ returns the shell and referenced embedded assets load.
3. An unknown /v1/ endpoint returns a JSON 404, not the SPA; a known exported dashboard route loads, while an unknown dashboard route returns its 404.
4. Default listening is loopback; explicit configuration overrides documented defaults.
5. Termination shuts down without leaked listeners/goroutines or hanging the verification run.
6. Formatting, go vet, Go tests, TypeScript checks, and frontend build all pass through the shared verification command.
7. An unclaimed dashboard routes to setup; a valid one-time code creates exactly one owner and an HttpOnly session, including under concurrent claim attempts.

Leave focused runnable httptest/process smoke checks for route separation, configuration precedence, embedded assets, and shutdown. Demonstrate the route-separation guard fails if the SPA fallback is incorrectly applied to /v1/.

**Done when:** the copied binary serves the embedded dashboard without the source/build tree, rejects unknown API paths as JSON, stops cleanly, and the documented verification command exits successfully.

Completed September 14, 2026. `./scripts/verify.sh` passed the documented acceptance suite, including race checks and the copied-binary process smoke test. See the [Phase 1 evidence](../phase-01-runtime-storage.md#evidence).

## Boundaries and risks

Do not implement provider adapters, generic service interfaces, policy stubs, fake successful inference responses, Docker publishing, or a production-release badge. F1.2 adds real SQLite readiness and locking; F1.3 adds recovery primitives; the owner-bootstrap slice remains intentionally smaller than Phase 2 authentication.

Primary risks: missing embedded build output, Next.js exported asset paths under /_/, incorrect exported-page resolution or API/HTML fallback, and a build that depends on a working directory. Record actual commands/results in the phase evidence when complete.
