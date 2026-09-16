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

Stable API keys may be encrypted locally or read through `env:NAME` and `file:/absolute/path`. Use `bearer-env:NAME` or `bearer-file:/absolute/path` for short-lived OAuth tokens. External references are resolved for each route selection; keep token minting and file rotation in the cloud identity agent rather than the protocol adapter.
