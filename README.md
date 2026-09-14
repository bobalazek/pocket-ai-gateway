# Pocket AI Gateway

Pocket AI Gateway is a self-hosted AI gateway in one executable. It embeds a Next.js dashboard, keeps operational state in local SQLite databases, and gives OpenAI, Anthropic, and Gemini clients separate native API namespaces backed by the provider you choose.

## Features

- **One executable** — the dashboard and migrations are embedded in the Go binary; Node.js is only needed to build it.
- **Three client protocols** — OpenAI Chat and stateless Responses, Anthropic Messages, and Gemini generateContent keep their own request, response, error, and streaming shapes.
- **Cross-provider translation** — route supported text, image input, JSON schema output, function tools, tool results, stop sequences, token usage, and supported incremental streams across OpenAI, Anthropic, and Gemini families.
- **Provider and model control** — configure encrypted provider credentials, upstream models, stable public model names, capabilities, and prices from the dashboard.
- **Users and keys** — owner, administrator, and member accounts; scoped application keys; key rotation, expiration, suspension, recovery, and session revocation.
- **Durable limits** — fixed windows, quotas, token buckets, concurrency controls, request/body/batch ceilings, and spend limits at instance, user, key, and connection scope.
- **Usage and request history** — token and cost accounting, unknown-usage reconciliation, historical repricing, protocol/target details, and a local dashboard.
- **Local by default** — no public telemetry, no hosted account, and no prompt capture by default.
- **Recovery tools** — protected offline snapshots, integrity-checked restore into a clean data directory, and owner recovery.

## API namespaces

| Client | Base URL | Authentication |
| --- | --- | --- |
| OpenAI SDK | `http://127.0.0.1:8080/api/openai/v1` | `Authorization: Bearer <gateway-key>` |
| Anthropic SDK | `http://127.0.0.1:8080/api/anthropic` | `x-api-key: <gateway-key>` |
| Google Gen AI SDK | `http://127.0.0.1:8080/api/gemini`, API version `v1beta` | `x-goog-api-key: <gateway-key>` |
| Gateway management | `http://127.0.0.1:8080/api/v1` | Browser session |

The URL selects the client protocol. A public model can point to a different upstream family while the client still receives its native wire format.

## Build once, run anywhere

Prerequisites: Go 1.27.1, Node.js 22 or newer, pnpm 10.30.3, and `curl` for verification.

```sh
./scripts/build.sh
./dist/pocket-ai-gateway serve
```

The build exports the dashboard, embeds it in the binary, and writes `dist/pocket-ai-gateway`. You can copy that single binary to another machine with the same operating system and architecture; it does not need the source tree or Node.js at runtime.

Open [http://127.0.0.1:8080/_/](http://127.0.0.1:8080/_/). A new data directory redirects to onboarding and prints the path of an owner-only `setup-code` file. Use that code to create the first owner account; Pocket AI Gateway never creates default credentials.

Runtime data is written to `./pocket_gateway_data` by default:

```text
pocket_gateway_data/
  system.db
  data.db
  master.key
  instance.lock
```

Keep the whole directory private and persistent. `system.db` stores identity, configuration, limits, and accounting. `data.db` stores projections. `master.key` protects provider credentials and must be backed up with the databases.

## Docker

Build and start the local Compose deployment:

```sh
docker compose up --build -d
docker compose logs gateway
```

Then open [http://localhost:8080/_/](http://localhost:8080/_/). The named volume preserves both databases and the credential key. The container runs as a non-root user with a read-only root filesystem.

For an HTTPS deployment, a system service, data-directory permissions, reverse-proxy examples, upgrades, and Docker volume handling, see the [deployment guide](docs/guides/deployment.md).

## First provider and request

1. Complete onboarding at `/_/setup/`.
2. Add a provider connection in **Providers** and save its credential.
3. Add its upstream model and publish a stable name in **Models**.
4. Create a scoped key in **API keys**.
5. Verify the setup in **Playground** or call the matching client namespace.

```sh
curl http://127.0.0.1:8080/api/openai/v1/chat/completions \
  -H "Authorization: Bearer $POCKET_GATEWAY_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "assistant",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 128
  }'
```

## Compatibility boundaries

Cross-protocol generation supports the shared, tested subset described above. Stateless OpenAI Responses requires `store: false`. Provider-owned conversations, background Responses, hosted tools, opaque reasoning blocks, and Gemini thought signatures are rejected before dispatch when they cannot be preserved. Embeddings and token counting use native capable targets; the gateway does not invent equivalent vector spaces or tokenizer results.

Anthropic streaming currently requires an Anthropic-compatible target because its first `message_start` event must include input usage that OpenAI and Gemini streams only report at completion. Cross-provider Anthropic requests remain available as ordinary JSON responses.

See the [API contract](docs/reference/api.md) and protocol guides for [OpenAI](docs/features/openai-compatible.md), [Anthropic](docs/features/anthropic-compatible.md), and [Gemini](docs/features/gemini-compatible.md).

## Operations

Create an offline snapshot while the server is stopped:

```sh
./dist/pocket-ai-gateway snapshot \
  --data-dir ./pocket_gateway_data \
  --output ./snapshot-001
```

Restore into an absent directory:

```sh
./dist/pocket-ai-gateway restore \
  --snapshot ./snapshot-001 \
  --data-dir ./restored_gateway_data
```

Run the complete local verification gate:

```sh
./scripts/verify.sh
```

More documentation: [architecture](docs/architecture/README.md), [deployment](docs/guides/deployment.md), [security and recovery](docs/guides/operations.md), [product requirements](docs/project/prd.md), [design system](DESIGN.md), and [agent-readable help](llms.txt).

## License

[MIT](LICENSE)
