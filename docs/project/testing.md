# Testing strategy

Every phase ships with tests for its observable behavior and failure boundaries. The full `scripts/verify.sh` entrypoint runs locally and for releases. The GitHub Verify workflow is manual-only and uses quick mode for format, lint, build, unit, integration, TypeScript, and SDK checks; race and copied-binary smoke checks remain in the full local gate.

The race suite allows 15 minutes per test binary. The gateway package exceeds Go's default 10-minute timeout on the local development machine; the longer timeout keeps the complete suite enabled without dropping coverage. See the [recorded baseline timings](../plan/phase-08-provider-api-expansion.md#incremental-parity-evidence--2026-09-18).

| Layer | Purpose | Required examples |
| --- | --- | --- |
| Unit | Pure rules and edge cases with fast, deterministic fixtures | Configuration precedence, protocol conversion, policy math, price selection, error mapping |
| Integration | Real boundaries without external paid services | SQLite migrations/locking/transactions, HTTP handlers, mock provider servers, concurrency, cancellation, corrupt input and restart recovery |
| End to end | What an operator or SDK experiences from the built artifact | Copied single binary, startup/shutdown, embedded assets, setup/login, key lifecycle, each client dialect, streams/tools, backup/restore |
| Browser | Dashboard journeys and accessibility | Manual release walkthrough on 360/768/1280 viewports: keyboard setup, profile/password changes, key issue/revoke, limits, audit and recovery discovery |
| Compatibility | Pinned official clients through the Go gateway and deterministic upstreams | OpenAI 7.15.0, Anthropic 0.125.0, and Google Gen AI 2.22.0 across every supported client/provider combination, Responses, streams, errors, and exact base URLs |

`./scripts/compose-e2e.sh` is the local container gate. It builds the production image and proves onboarding, persistent volumes, encrypted paired-store backup, clean-volume restore, login, and session survival across a container restart. The test uses disposable Compose volumes and deletes them on exit.

`./scripts/compose-e2e.sh --s3` adds an actual local S3 protocol server. It needs `jq`, OpenSSL, and curl with `--aws-sigv4` and `--fail-with-body` (7.76 or later). It uploads through the gateway with spaces, a plus sign, a percent sign, and Unicode in the prefix; independently downloads the object; checks its recorded SHA-256 and size; then restores that download through the production CLI. Rejected credentials must produce a visible failed backup job without breaking readiness. The same login/restart checks follow. The pinned historical MinIO image is only a disposable protocol regression fixture, not a deployment recommendation or production dependency; all credentials are synthetic and exposed ports bind to loopback. This proves local interoperability, not cloud IAM, TLS, bucket lifecycle policy, or off-host durability.

`./scripts/update-e2e.sh` separately checks standalone Linux updates using real executables. After `./scripts/build.sh`, it runs an isolated Linux container with read-only source/dependencies and no external network. A temporary TLS release server and signing key exercise the default dry-run, atomic installation, readiness with a configured public domain, and forced failure after both databases and the master key change. The test verifies restoration of the previous binary, both stores, and the key. Run this local gate after updater/CLI/storage changes and for release candidates; it is opt-in (`-tags=e2e`) and does not add GitHub jobs.

Coverage is risk based. Security, authorization, accounting, migrations, streaming, and recovery require negative and concurrency cases. Generated UI markup and trivial accessors do not need tests that merely repeat their implementation.

The [demo fixture](../guides/demo.md) has an ordinary Go test for data isolation, login, accounting, and cleanup. The [performance harness](benchmark.md) checks complete JSON/SSE responses and reconciled usage against a real standalone process. `./scripts/benchmark-linux.sh` measures a prebuilt native Linux artifact in a disposable, non-root, network-isolated container. Separate JSON/stream durations, per-minute RSS/goroutine windows, and cooldown measurements expose retained connections or growing background work; missing measurements fail. Recorded load results are separate from correctness tests.

Live provider tests are opt-in and never run billable inference automatically. Deterministic local mocks own CI. A phase cannot close with skipped acceptance tests unless the plan records the external prerequisite and a safe local substitute.
