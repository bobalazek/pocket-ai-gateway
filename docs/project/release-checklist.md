# Release checklist

- [x] Local build, format, lint, backend/frontend/SDK tests, race checks, and copied-binary smoke pass. [Recorded source and environment](../plan/README.md#local-verification--2026-09-22).
- [x] `./scripts/package.sh vX.Y.Z` produces Linux amd64/arm64 raw binaries, archives, and `SHA256SUMS`. (Verified with `v0.1.0-checklist`.)
- [x] Linux amd64/arm64 binaries execute `version`. (Verified under matching Docker platforms.)
- [x] The multi-platform image builds for linux/amd64 and linux/arm64.
- [x] The local production image persists `/data` across restart.
- [x] `./scripts/compose-e2e.sh` passes against the production image.
- [x] `./scripts/compose-e2e.sh --s3` verifies a real local S3 upload, independent download/checksum, rejected-credential failure reporting, and restore/login/restart from the downloaded archive. Cloud-account and off-host certification remain separate.
- [x] The copied-binary smoke completes clean onboarding, key issue, and encrypted backup/restore without the source tree.
- [x] Deterministic provider/routing tests cover provider setup and route preview; pinned official SDK suites cover the documented compatibility subset.
- [x] An encrypted backup restores into a clean Docker data volume with the matching key; the restored instance passes `/readyz` and sign-in.
- [x] Automated suites separately cover pre-migration snapshots, encrypted restore, and failed self-update rollback.
- [x] `./scripts/update-e2e.sh` checks real Linux CLI dry-run/apply, the production readiness probe with a public domain configured, and rollback after both stores and the master key change. Verified on Linux arm64 with disposable v1.0.0/v1.1.0 binaries and a deliberately broken signed v1.2.0 fixture; repeat on each final release architecture.
- [x] A [disposable example instance](../guides/demo.md) populates users, models, keys, priced usage, final failures, and seven days of history; 39 screenshots cover analytics, alerts, requests, providers, configuration, and administration. It cannot seed an existing data directory or call a real AI provider.
- [x] A [repeatable Linux resource check](benchmark.md#linux-beforeafter--september-22-2026) records the artifact/environment, 50 JSON requests/second, five minutes with 50 streams, per-minute memory/goroutine ranges, management responsiveness, and post-load cleanup. The fixed run settled all 10,714 requests without errors; peak RSS was 51.82 MiB and goroutines returned to 13 after cooldown. Claims remain bounded to this workload and host.
- [x] `llms.txt`, the [operator skill](../../skills/pocket-ai-gateway-ops/SKILL.md), deployment/backup/recovery guides, and the [API parity inventory](api-parity-inventory.md) describe the supported subset and its limits.
- [x] No unresolved security, data-loss, quota, credential, or false-compatibility finding remains in the source candidate review.

## Before making the source repository public

- [x] At `5fd2a1c`, scan all 185 reachable commits, 2,394 blobs, 51 historical Actions runs (106 log files), and tracked demo images. No credentials were found and Actions retains no artifacts. Rescan changes after that commit before publication.
- [x] The owner accepted public exposure of the commit-author email on 2026-09-25.
- [x] The repository became public on 2026-09-25 with private vulnerability reporting enabled, as `SECURITY.md` describes.

## Before publishing a tag

- [ ] Select the final committed candidate and rerun the local verification, Compose recovery, and Linux update gates on its release artifacts. The current working-tree verification does not attest a future tag.
- [ ] Record the candidate's source revision, environment, and performance results in [benchmark](benchmark.md); address any unmet performance target before claiming it. Publish exact tagged-artifact hashes in the release's `SHA256SUMS` and signed manifest, then link that immutable evidence from [compatibility](compatibility.md) after publication.
- [x] The confidential vulnerability-reporting route named in `SECURITY.md` is enabled.
- [ ] Configure the external Ed25519 signing secret and distribute its public key; verify the final `release-manifest.json` and detached signature. Local test keys are not release trust keys.
- [ ] Confirm repository publication/attestation permissions, then publish `SHA256SUMS`, the signed update manifest, SPDX SBOM, GitHub artifact attestation, release notes, and container provenance.
- [ ] After the first GHCR image push, make its package public and verify an unauthenticated image pull before advertising the container. Publishing the source repository does not make a new container package public.

No workflow is triggered by this checklist. Verify runs quick mode on pushes and pull requests; tag publication and release credentials are maintainer actions in [human tasks](human-tasks.md).

## Optional certification and future implementation

- Live calls for the named providers, cloud IAM flows, and off-host S3 endpoints remain unverified until credentials, exact operations/models, and a spending ceiling are supplied. The disposable local S3 server is verified separately above. [Provider evidence](provider-certification.md) separates local conformance from live certification.
- Remote libSQL/Turso is not implemented. It needs a driver and tested durability, fencing, and recovery before service certification; see [ADR-051](decisions/2026-09-16-close-v01-with-local-sqlite.md).
- Full vendor API parity is not delivered. Remaining file/upload/cache resources, provider-owned batches, hosted tools/reasoning translation, semantic search, and additional realtime transports remain explicitly tracked in the [API parity inventory](api-parity-inventory.md). They are future implementation work, not missing credentials for existing features.
