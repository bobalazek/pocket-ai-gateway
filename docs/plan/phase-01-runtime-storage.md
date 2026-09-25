# Phase 1 — Executable, storage, and recovery foundation

Status: complete. [Plan index](README.md)

**Outcome:** an offline-capable local server that owns one data directory and serves its embedded dashboard. No provider traffic, user data exposure, or production-readiness claim. Route roots are /api/openai, /api/anthropic, /api/gemini, and separate gateway/auth/admin resources.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| F1.1 Runtime/build | Go entry point, serve/version, loopback listener, flags/environment precedence, signal shutdown; built binary serves assets after source/build folders are removed | RUN-01, OPS-02 |
| F1.2 Storage/locking | Pin reviewed driver, confirm SQLite fix/version and target architectures; initialize system.db/data.db, foreign keys, WAL, busy timeout, per-store migration checksums and sqlc, protected files, OS lock; second process refuses access | RUN-01, DATA-01, SEC-01 |
| F1.3 Snapshot/recovery probe | Implement protected paired offline snapshot and integrity/restore proof on disposable data; simulated interruption cannot erase the source; newer schema refused | OPS-01, DATA-01 |
| F1.4 Verification entry point | One full local/release verification command with formatting/vet/tests/UI build and embedded artifact smoke; routine CI runs its quick mode without race or copied-binary smoke checks | NFR-06, OPS-02 |
| F1.5 Owner bootstrap | Route an unclaimed dashboard to `/_/setup/`; atomically create the sole owner and an HttpOnly session from the first same-origin claim; no default credentials | IAM-01, SEC-01, UI-01 |

Frontend: structural shell under /_/, first-run owner setup, minimal safe readiness/status output, locally served assets, API 404 behavior, typed same-origin API client, and black/white visual tokens with a restrained blue accent. Provider settings stay unavailable until their protected APIs exist.

**Exit gate:** clean start/restart, first-run redirect and owner claim, concurrent claim safety, lock contention, migration failure, asset embedding, offline snapshot/restore, and graceful shutdown checks pass. Capture actual engine version and supported build targets.

**Blast radius:** all future persistent state and release artifacts. Do not add speculative providers or business tables here.

## Evidence

Completed September 14, 2026 on macOS arm64 with Go 1.27.1, Node.js 22.22.1, pnpm 10.30.3, and bundled SQLite 3.53.4.

- `./scripts/verify.sh` passed formatting, vet, Go and browser-client unit/integration tests, race tests, TypeScript, frontend request-boundary enforcement, production dashboard build, sqlc generation drift, supported-target cross-builds, and copied-binary end-to-end smoke checks.
- Cross-builds passed for macOS amd64/arm64 and Linux amd64/arm64. Windows remains a Phase 7 native ACL/artifact test target.
- The smoke test passed embedded dashboard/setup/status/assets and `llms.txt`, first-run owner/session APIs, protocol-specific JSON 404s, second-process lock refusal, graceful termination, version output, and paired snapshot/restore.
- Independent runtime, storage/security, dashboard, and final reviews passed after CSP, migration preflight, snapshot provenance/generation, restore staging, setup retry/logging, accessibility, sync, and format-version findings were fixed.
- Manual dashboard and first-run setup walkthroughs passed at 360, 768, and 1280 pixels with no browser warnings or errors.
