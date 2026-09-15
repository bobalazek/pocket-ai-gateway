# Pocket AI Gateway

**A self-hosted AI gateway and control panel in one executable.**

Pocket AI Gateway gives applications stable OpenAI, Anthropic, and Gemini API surfaces while routing requests to the providers and models you control. It keeps users, keys, limits, routing, usage, and audit history on your own server in local SQLite databases.

The dashboard and database migrations are embedded in the Go binary. Build it once, copy one file, and run it without Node.js or a separate database service.

## What it provides

| Area | Included |
| --- | --- |
| Client APIs | Separate OpenAI, Anthropic, and Gemini namespaces with native request, response, error, and streaming shapes |
| Providers | OpenAI, Anthropic, Gemini, OpenRouter, Ollama, Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, Perplexity, Azure OpenAI, Bedrock, Vertex AI, and custom compatible endpoints |
| Routing | Fixed, ordered fallback, weighted, lowest estimated cost, and observed latency strategies |
| Access | Multiple administrators and members, browser sessions, scoped application keys, rotation, expiration, and revocation |
| Control | Per-instance, user, key, and connection request, token, concurrency, quota, and spend limits |
| Visibility | Dashboard, request history, token and cost accounting, routing decisions, status, and audit log |
| Operations | Encrypted credentials, portable configuration, scheduled local or S3-compatible backups, verified restore, and owner recovery |
| Privacy | Local storage, no hosted account, no public telemetry, and no prompt capture in ordinary request history; stored Responses retain their requested content |

Public model names decouple applications from upstream providers. An application can keep using its OpenAI client while an administrator moves the model to Anthropic, Gemini, or another compatible provider, provided the requested features can be translated safely.

## Quick start

You need Go 1.27.1, Node.js 22 or newer, pnpm 10.30.3, and `curl` to build from source.

```sh
git clone https://github.com/bobalazek/pocket-ai-gateway.git
cd pocket-ai-gateway
./scripts/build.sh
./dist/pocket-ai-gateway serve
```

Open [http://127.0.0.1:8080/_/](http://127.0.0.1:8080/_/). On a new installation, the dashboard opens onboarding and the server prints the location of a one-time setup code. Use it to create the first owner account; the gateway never creates a default password.

The build produces a standalone executable at `dist/pocket-ai-gateway`. Copy that file to a machine with the same operating system and architecture and run it there. The source tree, Node.js, and pnpm are build-time requirements only.

Runtime state is stored in `./pocket_gateway_data` unless `--data-dir` is set:

```text
pocket_gateway_data/
  system.db
  data.db
  master.key
  instance.lock
```

Keep this directory private and persistent. The databases hold identity, configuration, limits, accounting, and projections; `master.key` protects provider credentials.

## Connect a provider

After onboarding:

1. Add a provider connection in **Providers** and save its credential.
2. Add an upstream model and publish a stable name in **Models**.
3. Choose a routing strategy and add any fallback targets.
4. Create a scoped application key in **API keys**.
5. Test the route in **Playground** or use one of the client endpoints below.

```sh
export POCKET_GATEWAY_KEY="your-gateway-key"

curl http://127.0.0.1:8080/api/openai/v1/chat/completions \
  -H "Authorization: Bearer $POCKET_GATEWAY_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "assistant",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 128
  }'
```

## Client API surfaces

The base URL selects the client protocol; protocol families are never mixed at one path.

| Client | Base URL | Gateway authentication |
| --- | --- | --- |
| OpenAI SDK | `http://127.0.0.1:8080/api/openai/v1` | `Authorization: Bearer <key>` |
| Anthropic SDK | `http://127.0.0.1:8080/api/anthropic` | `x-api-key: <key>` |
| Google Gen AI SDK | `http://127.0.0.1:8080/api/gemini` with API version `v1beta` | `x-goog-api-key: <key>` |
| Management API | `http://127.0.0.1:8080/api/v1` | Browser session |

Supported cross-protocol generation includes text, image input, JSON schema output, function tools and results, stop sequences, token usage, and compatible incremental streams. OpenAI Responses also supports gateway-owned storage, background execution, polling, cancellation, input-item listing, and deletion. Embeddings and token counting stay on targets that natively support them so the gateway does not invent vector spaces or tokenizer results.

See the detailed [compatibility matrix](docs/project/compatibility.md) and API guides for [OpenAI](docs/features/openai-compatible.md), [Anthropic](docs/features/anthropic-compatible.md), and [Gemini](docs/features/gemini-compatible.md).

## Dashboard

The embedded dashboard covers:

- system status and diagnostics;
- usage, cost, latency, and request history;
- users, roles, sessions, recovery, and account settings;
- application keys and their grants, limits, and spend controls;
- provider connections, credentials, upstream models, and public models;
- routing strategies, route previews, fallback outcomes, and pricing;
- audit events, data retention, configuration export/import, and backups.

All browser calls go through the typed management API client. The dashboard does not connect to the databases or providers directly.

## Docker

Build and start the included local deployment:

```sh
docker compose up --build -d
docker compose logs gateway
```

Open [http://localhost:8080/_/](http://localhost:8080/_/). Named volumes preserve the data and backup directories. The container runs as a non-root user with a read-only root filesystem.

For HTTPS, systemd, Caddy, data-directory permissions, multi-platform images, upgrades, backups, and restore, use the [deployment guide](docs/guides/deployment.md).

## Back up and restore

Set a separately stored archive key, stop the gateway, and create an encrypted backup:

```sh
export POCKET_AI_GATEWAY_BACKUP_KEY="$(openssl rand -base64 32)"

./dist/pocket-ai-gateway backup \
  --data-dir ./pocket_gateway_data \
  --output ./gateway.pagbak
```

Restore into a directory that does not exist yet:

```sh
./dist/pocket-ai-gateway restore-backup \
  --archive ./gateway.pagbak \
  --data-dir ./restored_gateway_data
```

The archive contains both databases and the credential master key. Restore authenticates the archive, validates file hashes, and checks database integrity before replacing service data. Scheduled local and S3-compatible backups are available in **Settings**.

## Build and verify

```sh
./scripts/build.sh
./scripts/verify.sh
```

`build.sh` exports and embeds the dashboard, then builds the executable. `verify.sh` checks formatting, generated code and notices, API contracts, frontend boundaries, unit and integration tests, race detection, the production dashboard build, the embedded runtime, and the packaged smoke path.

Create release archives for supported platforms with:

```sh
./scripts/package.sh v0.1.0
```

## Documentation

- [Deployment](docs/guides/deployment.md)
- [Operations and recovery](docs/guides/operations.md)
- [API reference](docs/reference/api.md)
- [Provider adapters](docs/guides/adapters.md)
- [Provider compatibility and certification](docs/project/provider-certification.md)
- [Architecture](docs/architecture/README.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Agent-readable help](llms.txt)

## Project status

Pocket AI Gateway is under active development. The compatibility matrix records the exact surfaces covered by deterministic tests and calls out known boundaries. Live provider certification requires provider credentials and available models and is reported separately from local protocol conformance.

## License

[MIT](LICENSE)
