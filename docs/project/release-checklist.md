# Release checklist

- [ ] `./scripts/verify.sh` passes from a clean checkout.
- [ ] `./scripts/package.sh vX.Y.Z` produces Linux amd64/arm64 raw binaries, archives, and `SHA256SUMS`.
- [ ] Linux amd64/arm64 binaries execute `version`.
- [ ] The multi-platform image builds for linux/amd64 and linux/arm64 and persists `/data` across restart.
- [ ] A clean instance completes onboarding, provider setup, route preview, key issue, and deterministic SDK calls.
- [ ] An encrypted backup restores on a clean host with the matching key; the restored instance passes `/readyz` and sign-in.
- [ ] Upgrade migrations and matching-binary rollback through a restored backup are exercised.
- [ ] The external Ed25519 signing secret is configured; `release-manifest.json` and its detached signature verify with the distributed public key.
- [ ] `SHA256SUMS`, signed update manifest, SPDX SBOM, GitHub artifact attestation, release notes, and container provenance are published.
- [ ] On a disposable Linux host, `update` dry-run and `update --apply` pass; a forced readiness failure restores the previous binary and data snapshot.
- [ ] Compatibility and benchmark records name the exact artifact and environment.
- [ ] No unresolved security, data-loss, quota, credential, or false-compatibility finding remains.
