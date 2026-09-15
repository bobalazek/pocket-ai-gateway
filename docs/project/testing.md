# Testing strategy

Every phase ships with tests for its observable behavior and failure boundaries. The full `scripts/verify.sh` entrypoint runs locally and for releases. Routine CI uses its quick mode for format, lint, build, unit, integration, TypeScript, and SDK checks; race and copied-binary smoke checks remain in the full gate.

| Layer | Purpose | Required examples |
| --- | --- | --- |
| Unit | Pure rules and edge cases with fast, deterministic fixtures | Configuration precedence, protocol conversion, policy math, price selection, error mapping |
| Integration | Real boundaries without external paid services | SQLite migrations/locking/transactions, HTTP handlers, mock provider servers, concurrency, cancellation, corrupt input and restart recovery |
| End to end | What an operator or SDK experiences from the built artifact | Copied single binary, startup/shutdown, embedded assets, setup/login, key lifecycle, each client dialect, streams/tools, backup/restore |
| Browser | Dashboard journeys and accessibility | Playwright on 360/768/1280 viewports, keyboard setup, profile/password changes, key issue/revoke, limits, audit and recovery discovery |
| Compatibility | Pinned official clients through the Go gateway and deterministic upstreams | OpenAI 7.15.0, Anthropic 0.125.0, and Google Gen AI 2.22.0 across every supported client/provider combination, Responses, streams, errors, and exact base URLs |

Coverage is risk based. Security, authorization, accounting, migrations, streaming, and recovery require negative and concurrency cases. Generated UI markup and trivial accessors do not need tests that merely repeat their implementation.

Live provider tests are opt-in and never run billable inference automatically. Deterministic local mocks own CI. A phase cannot close with skipped acceptance tests unless the plan records the external prerequisite and a safe local substitute.
