# Provider adapter guide

An adapter preserves one upstream protocol family. Add a new native family only when its authentication, URL layout, requests, responses, errors, usage, tools, and streams cannot be represented by an existing adapter.

1. Add the adapter identifier and explicit capabilities in `internal/features/providers`.
2. Normalize and validate its base URL through the existing egress boundary. Never inherit client credentials or forward hop-by-hop headers.
3. Map each supported operation in the gateway handler. Reject unsupported features before admission or upstream dispatch.
4. Add deterministic request, response, error, cancellation, timeout, and streaming fixtures.
5. Exercise the official client against the public protocol namespace when the adapter affects client compatibility.
6. Update the protocol guide, management OpenAPI catalog, compatibility record, and dashboard preset only after tests pass.

OpenAI-compatible branding alone is not evidence. Record the provider, API version, model, date, operations exercised, and any divergence. Live tests must be opt-in and budget limited.
