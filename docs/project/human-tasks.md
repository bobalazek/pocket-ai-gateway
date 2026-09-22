# Human tasks

Resolved choices belong in [decisions](decisions/README.md). Implementation work belongs in [the plan](../plan/README.md).

## Release prerequisites

- [ ] Enable GitHub private vulnerability reporting or replace the documented private contact before the first public tag — Owner: project owner/maintainer — Unblocks: accepting confidential reports.
- [ ] Configure the external Ed25519 release-signing secret and distribute its public verification key — Owner: maintainer — Unblocks: signed public update manifests. Disposable keys used by local tests are never release keys.
- [ ] Grant the release workflow artifact/container publication access and verify repository attestation settings — Owner: maintainer — Unblocks: publishing verified release artifacts.

## Optional real-service certification

- [ ] Supply provider credentials, exact models/operations, and an explicit spending ceiling — Owner: project owner — Unblocks: live certification, prioritizing OpenAI, Anthropic, Gemini, OpenRouter, Z.AI, MiniMax, Together, and Replicate. Deterministic tests do not certify a live account/model.
- [ ] Supply a disposable off-host S3-compatible bucket/endpoint and credentials — Owner: project owner — Unblocks: cloud credentials/TLS/policy and off-host upload/download/restore certification. Encrypted archive, S3 contract, and real local S3 upload/download/restore tests already pass without a cloud account.
- [ ] Supply a disposable Turso account only after the remote backend is implemented — Owner: project owner — Unblocks: service-specific recovery/fencing tests. An account alone does not add remote storage support.

MIT, the product name, multiple admins, local-first storage, S3 backup capability, initial providers, and no public telemetry are resolved and are not asked again.
