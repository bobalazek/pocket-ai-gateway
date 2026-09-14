# Human tasks

Resolved choices belong in [decisions](decisions/README.md). Implementation work belongs in [the plan](../plan/README.md).

## Pending clarifications

- [ ] Clarify asynchronous inference — Owner: project owner — Unblocks: whether durable background job submission/polling/cancellation is initial scope. Default plan includes ordinary responses and SSE streaming; background jobs are recorded as a separate proposed extension.

## Release prerequisites

- [ ] Choose a real private security-reporting channel — Owner: project owner/maintainer — Unblocks: SECURITY.md and public release.
- [ ] Provide optional provider/Turso/S3 test accounts and explicit live-test spending ceiling when integration testing begins — Owner: project owner — Unblocks: real-service certification; deterministic mock work is unblocked.
- [ ] Configure release signing identity/provenance and artifact publication access — Owner: maintainer — Unblocks: publishing verified release artifacts.

MIT, the product name, multiple admins, local-first storage, S3 backup capability, initial providers, and no public telemetry are resolved and are not asked again.
