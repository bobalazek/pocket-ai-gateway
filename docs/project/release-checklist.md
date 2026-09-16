# Release checklist

- [x] `./scripts/verify.sh` passes for the committed candidate tree. (Local evidence: 2026-09-16.)
- [x] `./scripts/package.sh vX.Y.Z` produces Linux amd64/arm64 raw binaries, archives, and `SHA256SUMS`. (Verified with `v0.1.0-checklist`.)
- [x] Linux amd64/arm64 binaries execute `version`. (Verified under matching Docker platforms.)
- [x] The multi-platform image builds for linux/amd64 and linux/arm64.
- [x] The local production image persists `/data` across restart.
- [x] `./scripts/compose-e2e.sh` passes against the production image.
- [x] The copied-binary smoke completes clean onboarding, key issue, and encrypted backup/restore without the source tree.
- [x] Deterministic provider/routing tests cover provider setup and route preview; pinned official SDK suites cover the documented compatibility subset.
- [x] An encrypted backup restores into a clean Docker data volume with the matching key; the restored instance passes `/readyz` and sign-in.
- [x] Automated suites separately cover pre-migration snapshots, encrypted restore, and failed self-update rollback.
- [ ] The external Ed25519 signing secret is configured; `release-manifest.json` and its detached signature verify with the distributed public key.
- [ ] `SHA256SUMS`, signed update manifest, SPDX SBOM, GitHub artifact attestation, release notes, and container provenance are published.
- [ ] On a disposable Linux host, `update` dry-run and `update --apply` pass; a forced readiness failure restores the previous binary and data snapshot.
- [ ] Compatibility and benchmark records name the exact artifact and environment.
- [x] No unresolved security, data-loss, quota, credential, or false-compatibility finding remains in the source candidate review.

Unchecked items are tag-publication gates that require maintainer secrets, repository permissions, a disposable Linux release host, or final release metadata. They do not run in routine GitHub Actions.
