# Pocket AI Gateway

A self-hosted gateway for OpenAI, Anthropic, Gemini, and System One decision clients. One Go process serves the API and admin dashboard, stores configuration and usage in local SQLite, and routes requests through model names you control.

[![Full desktop dashboard showing synthetic requests and spend](docs/images/demo-page-overview.png)](docs/images/demo-page-overview.png)

*The embedded dashboard with disposable example data. [Browse the desktop and mobile gallery](docs/guides/demo.md#screenshots).*

## Try it locally

The gateway is a single process and a single container. With Docker installed:

```sh
git clone https://github.com/bobalazek/pocket-ai-gateway.git
cd pocket-ai-gateway
docker compose up --build -d
```

Compose only builds that one container and attaches its volumes; the [deployment guide](docs/guides/deployment.md#docker) shows the equivalent `docker run`, and [Run one executable](#run-one-executable) needs no Docker at all.

Open [http://localhost:8080/_/](http://localhost:8080/_/) and create the first owner account. In the dashboard:

1. Add a provider connection and its credential.
2. Add an upstream model with Chat capability, then publish `assistant` as a public model with Chat capability.
3. Create an API key with `chat:generate`, a model pattern matching `assistant`, and the provider connection ID. The secret is shown once.

```sh
export POCKET_AI_GATEWAY_KEY="paste-your-key"
curl http://localhost:8080/api/openai/v1/chat/completions \
  -H "Authorization: Bearer $POCKET_AI_GATEWAY_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"assistant","messages":[{"role":"user","content":"Hello"}]}'
```

Use the model ID you published in place of `assistant`. The [setup guide](docs/guides/setup.md) explains every step, including all key scopes and limits. Compose binds to localhost and keeps data in named volumes. For a remote server, follow the [deployment guide](docs/guides/deployment.md) to add HTTPS, set the public URL in a Compose override, and configure encrypted backups.

## What it does

- **Separate client APIs.** OpenAI, Anthropic, and Gemini SDKs connect through their own URL namespaces. Supported operations have tested request, response, and streaming behavior.
- **One model name, multiple targets.** Publish stable model IDs and route them with fixed, fallback, weighted, cost, or observed-latency strategies.
- **Keys and limits.** Manage users and scoped API keys, then apply request, token, concurrency, quota, and spend policies by instance, user, key, or provider connection.
- **Usage you can trace.** Compare traffic, errors, and known spend by API key, model, and provider; inspect attempts, cache usage, price versions, cost restatements, and audit events. Local status alerts flag sustained failures and backup or storage problems.
- **Local operations.** Provider secrets are encrypted. The server includes backup and restore tools, local or S3-compatible scheduled backups, and no public telemetry.
- **Typed decisions.** TypeSafe Jev and self-hosted Laya answer yes/no, choice, and score questions with calibrated probabilities through `/api/systemone`, with the same keys, limits, and accounting. See [System One decisions](docs/features/systemone-compatible.md).
- **More than text.** The tested subset includes image and audio operations, live WebSocket transports, durable media jobs, and trusted JavaScript transforms for custom adapters.

Built-in presets include OpenAI, Anthropic, Gemini, OpenRouter, Z.AI, MiniMax, Ollama, Together, Replicate, TypeSafe, and Laya. Each preset fills its reviewed provider base URL; one connection can hold many upstream models. A preset does not imply that every model supports every operation. See [provider URLs and evidence](docs/project/provider-certification.md), the [Replicate model guide](docs/guides/adapters.md), and the [compatibility matrix](docs/project/compatibility.md).

## SDK base URLs

| Client | Base URL |
| --- | --- |
| OpenAI | `http://localhost:8080/api/openai/v1` |
| Anthropic | `http://localhost:8080/api/anthropic` |
| Google Gen AI | `http://localhost:8080/api/gemini` with API version `v1beta` |
| TypeSafe (System One) | `http://localhost:8080/api/systemone` |

Application requests use a scoped gateway API key. Dashboard sign-in uses a separate browser session.

## Explore the dashboard

These screens use synthetic traffic in a disposable local instance. No real provider credential or paid AI request is involved; a deployed gateway shows its own data. Run `./scripts/demo.sh` to explore it, or browse the [full screenshot gallery](docs/guides/demo.md#screenshots).

| Analytics | Failed requests |
| --- | --- |
| [![Analytics with request volume, failed requests, spend, and error rate](docs/images/demo-preview-analytics.png)](docs/images/demo-page-analytics.png) | [![Request history filtered to failed requests](docs/images/demo-preview-request-errors.png)](docs/images/demo-page-request-errors.png) |

| Provider connection | Model routing |
| --- | --- |
| [![Provider base URL and upstream models under one connection](docs/images/demo-providers-connection.png)](docs/images/demo-page-providers.png) | [![Public model with routing strategy and upstream targets](docs/images/demo-models-route.png)](docs/images/demo-page-models.png) |

| Usage | Status and alerts |
| --- | --- |
| [![Usage totals, token chart, and known spend](docs/images/demo-preview-usage.png)](docs/images/demo-page-usage.png) | [![Runtime checks and local high-error alert](docs/images/demo-preview-status.png)](docs/images/demo-page-status.png) |

[OpenAI](docs/images/demo-providers-preset-openai.png) and [Replicate](docs/images/demo-providers-preset-replicate.png) preset previews show their base URLs and supported operations; selecting a preset does not connect or call its provider.

## Run one executable

To build from source, install Go 1.27.1, Node.js 22 or newer, and pnpm 10.30.3, then run:

```sh
./scripts/build.sh
./dist/pocket-ai-gateway serve
```

The executable contains the dashboard and database migrations. It listens on `127.0.0.1:8080` and stores data in `./pocket_gateway_data` by default. Open `http://127.0.0.1:8080/_/` for first-time setup. Build tools are unnecessary on the runtime host when you copy a binary for the same operating system and architecture.

The [deployment guide](docs/guides/deployment.md) covers Docker, systemd, reverse proxies, backups, and upgrades. Backups require a separate `POCKET_AI_GATEWAY_BACKUP_KEY`; keep that key and a copy of each archive outside the server.

## Documentation

- [Set up a gateway](docs/guides/setup.md): roles, providers, models, prices, API key scopes, and limits
- [Client skill for AI agents](skills/pocket-ai-gateway/SKILL.md)
- [API reference and supported operations](docs/reference/api.md)
- [Deployment and recovery](docs/guides/deployment.md)
- [Architecture](docs/architecture/README.md)
- [Contributing](CONTRIBUTING.md) and [security policy](SECURITY.md)

## Project status

Pocket AI Gateway is [MIT-licensed](LICENSE) and under active development. The implemented APIs cover documented, tested subsets of vendor contracts; live provider certification and release signing are pending. See the [release checklist](docs/project/release-checklist.md) for current status.
