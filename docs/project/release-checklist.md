# Release checklist

- [ ] `./scripts/verify.sh` passes from a clean checkout.
- [ ] `./scripts/package.sh vX.Y.Z` produces Linux amd64/arm64 archives and `SHA256SUMS`.
- [ ] Linux amd64/arm64 binaries execute `version`.
- [ ] The multi-platform image builds for linux/amd64 and linux/arm64 and persists `/data` across restart.
- [ ] A clean instance completes onboarding, provider setup, route preview, key issue, and deterministic SDK calls.
- [ ] An encrypted backup restores on a clean host with the matching key; the restored instance passes `/readyz` and sign-in.
- [ ] Upgrade migrations and matching-binary rollback through a restored backup are exercised.
- [ ] `SHA256SUMS`, SPDX SBOM, GitHub artifact attestation, release notes, and container provenance are published.
- [ ] Compatibility and benchmark records name the exact artifact and environment.
- [ ] No unresolved security, data-loss, quota, credential, or false-compatibility finding remains.
