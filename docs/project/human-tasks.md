# Human tasks

Resolved choices belong in [decisions](decisions/README.md). Implementation work belongs in [the plan](../plan/README.md).

## Release prerequisites

- [x] Accept public exposure of the existing commit-author email — accepted by the project owner on 2026-09-25; no history rewrite.
- [x] Make the repository public and enable GitHub private vulnerability reporting — done on 2026-09-25; the Verify workflow was re-enabled for pushes and pull requests.
- [x] Configure the Ed25519 release-signing secret and publish its public key — done on 2026-09-25. Keep the seed backup in a password manager, not on disk.
- [x] Release workflow publication and attestation — verified by publishing `v0.1.0-alpha.1` on 2026-09-25.
- [ ] After the first GHCR image push, set the package to public and verify an anonymous pull — Owner: maintainer — Unblocks: public container installation. GitHub creates new container packages as private by default.

## Optional real-service certification

- [ ] Supply provider credentials, exact models/operations, and an explicit spending ceiling — Owner: project owner — Unblocks: live certification, prioritizing OpenAI, Anthropic, Gemini, OpenRouter, Z.AI, MiniMax, Together, and Replicate. Deterministic tests do not certify a live account/model.
- [ ] Supply a disposable off-host S3-compatible bucket/endpoint and credentials — Owner: project owner — Unblocks: cloud credentials/TLS/policy and off-host upload/download/restore certification. Encrypted archive, S3 contract, and real local S3 upload/download/restore tests already pass without a cloud account.
- [ ] Supply a disposable Turso account only after the remote backend is implemented — Owner: project owner — Unblocks: service-specific recovery/fencing tests. An account alone does not add remote storage support.

MIT, the product name, multiple admins, local-first storage, S3 backup capability, initial providers, and no public telemetry are resolved and are not asked again.
