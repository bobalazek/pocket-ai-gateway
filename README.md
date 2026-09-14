# Pocket AI Gateway

An open-source, local-first foundation for an AI gateway: one executable, an embedded dashboard, and a protected SQLite data directory. The planned gateway will connect providers, manage users and application keys, enforce limits, and expose separate OpenAI, Anthropic, and Gemini API namespaces.

The dashboard foundation uses Next.js, Tailwind CSS, and source-owned shadcn components. See [DESIGN.md](DESIGN.md) for its interface rules, [llms.txt](llms.txt) for concise agent-readable help, and the [operator skill](skills/pocket-ai-gateway-ops/SKILL.md) for agent-assisted health and usage checks.

**Status: Phase 2 complete. The local runtime includes owner onboarding, login and recovery, multiple administrators, member lifecycle, active-session controls, and scoped rotating API keys. Provider traffic, limits, and inference remain planned. There is no published release yet.**

Start with the [product requirements](docs/project/prd.md) and [implementation phases](docs/plan/README.md). The [documentation index](docs/README.md) links the architecture, API boundaries, data model, dashboard, and recovery design.

Foundation: Go, local SQLite by default, optional remote libSQL/Turso, and a Next.js/TypeScript dashboard. The recommended single-process build exports the dashboard as static assets and embeds them in Go; all runtime APIs remain in Go. Docker packages the same runtime.

## Build and run

Prerequisites: Go 1.27.1, Node.js 22 or newer, pnpm 10.30.3, and `curl` for the smoke check.

~~~sh
./scripts/build.sh
./dist/pocket-ai-gateway serve
~~~

The server listens on `127.0.0.1:8080`, writes `system.db` and `data.db` under `./pocket_gateway_data`, and serves the dashboard at `http://127.0.0.1:8080/_/`. Flags override `POCKET_AI_GATEWAY_LISTEN` and `POCKET_AI_GATEWAY_DATA_DIR`. A network-facing bind also requires `--public-url https://gateway.example.com`; this pins host/origin checks and secure cookies without trusting forwarded headers.

On a new data directory, startup prints the setup URL and the path to an owner-only file containing a short-lived code. The first dashboard visit opens `/_/setup/`; enter that code to create the owner account. No default credentials are created.

Create and restore a Phase 1 offline snapshot while the server is stopped:

~~~sh
./dist/pocket-ai-gateway snapshot --data-dir ./pocket_gateway_data --output ./snapshot-001
./dist/pocket-ai-gateway restore --snapshot ./snapshot-001 --data-dir ./restored_gateway_data
~~~

Restore deliberately requires an absent destination directory. These protected paired snapshots are local recovery primitives; encrypted portable archives and S3-compatible backups arrive in Phase 7.

If the owner loses their password, stop the server and create a one-time recovery code:

~~~sh
./dist/pocket-ai-gateway owner-reset --data-dir ./pocket_gateway_data
~~~

The command revokes owner sessions and writes the code to the protected `recovery-code` file. Enter it on `/_/activate/`.

Run the complete local gate, including generated-query drift, tests, supported-target builds, copied-binary routes, locking, shutdown, and snapshot/restore smoke checks:

~~~sh
./scripts/verify.sh
~~~

Licensed under [MIT](LICENSE). Architecture decisions live in [docs/project/decisions](docs/project/decisions/README.md); implementation phases live in [docs/plan](docs/plan/README.md).
