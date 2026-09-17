# Provider adapter guide

An adapter preserves one upstream protocol family. Add a new native family only when its authentication, URL layout, requests, responses, errors, usage, tools, and streams cannot be represented by an existing adapter.

1. Add the adapter identifier and explicit capabilities in `internal/features/providers`.
2. Normalize and validate its base URL through the existing egress boundary. Never inherit client credentials or forward hop-by-hop headers.
3. Map each supported operation in the gateway handler. Reject unsupported features before admission or upstream dispatch.
4. Add deterministic request, response, error, cancellation, timeout, and streaming fixtures.
5. Exercise the official client against the public protocol namespace when the adapter affects client compatibility.
6. Update the protocol guide, management OpenAPI catalog, compatibility record, and dashboard preset only after tests pass.

OpenAI-compatible branding alone is not evidence. Record the provider, API version, model, date, operations exercised, and any divergence. Live tests must be opt-in and budget limited.

Built-in presets pin reviewed HTTPS base URLs and list only operations stated in current provider documentation. The preset selects an existing adapter; it does not bypass model capabilities, key grants, limits, routing, egress checks, accounting, or compatibility validation. See [provider certification](../project/provider-certification.md) for the evidence boundary.

Provider brands that use the same wire contract stay as entries in `internal/features/providers/presets.go`. OpenRouter, Z.AI, MiniMax, Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, and Perplexity therefore share the `openai_compatible` adapter. Add a provider package only when a tested protocol difference cannot be represented by preset metadata or a small dispatch rule.

Stable API keys may be encrypted locally or read through `env:NAME` and `file:/absolute/path`. Use `bearer-env:NAME` or `bearer-file:/absolute/path` for short-lived OAuth tokens. External references are resolved for each route selection; keep token minting and file rotation in the cloud identity agent rather than the protocol adapter.

## Custom JavaScript transforms

A custom OpenAI-compatible connection may attach request and response transforms at `/api/v1/connections/{id}/adapter-script`. Each script is a JavaScript function expression and runs in a fresh embedded Goja VM for at most 50 ms. Scripts receive JSON only and have no filesystem, process, module-loader, timer, or network globals. Source, stack, input, output, and wall time are limited, and the gateway still owns the destination base URL, credentials, redirect policy, timeout, and response limits.

Scripts are trusted owner/admin configuration, not an isolation boundary for third-party code. Goja shares the server process heap and does not provide a hard per-invocation heap limit, so a malicious or defective script can exhaust server memory. Review scripts before activation and never import untrusted source.

The request function receives `{ operation, model, method, path, headers, body }` and may return any of `{ method, path, headers, body }`. The path must remain relative. The response function receives `{ operation, model, status, headers, body }` and may return any of `{ status, headers, body }`; status must remain successful. Authentication, cookies, forwarding headers, hop-by-hop headers, and content length cannot be set by scripts. Streaming and request transforms over multipart bodies are rejected before dispatch. Keep credentials out of script source and use the connection credential field.

For a fixed-route public model with the `media_jobs` capability, `POST /api/v1/media/jobs` invokes the same synchronous scripts with operation `media/jobs`. The request transform maps create, poll, and cancel to the provider's relative HTTP paths. The response transform must normalize JSON to `{ id, status, output, cost_usd? }`; status is `queued`, `starting`, `processing`, `in_progress`, `succeeded`, `completed`, `failed`, `aborted`, `canceled`, or `cancelled`. The gateway owns polling, bounded transient-error retries, encrypted storage, authorization, SSRF checks, redirects, limits, and accounting.
