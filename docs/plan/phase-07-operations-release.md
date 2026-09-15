# Phase 7 — Operations, security, and v0.1 release

Status: complete. [Plan index](README.md)

Depends on beta. Some operational groundwork begins in phase 1; this phase completes it for distribution.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| O7.1 Backup/restore | Paired encrypted archives, scheduled local/S3-compatible jobs, upload/checksum/retry/retention status, integrity checks, secret-version consistency, offline staged restore and recovery-mode reconciliation | OPS-01, SEC-01 |
| O7.2 Configuration/retention | Transactional preview/import/export, bounded metadata/capture/aggregate/audit retention, no enforcement-counter loss | OPS-01, DATA-01 |
| O7.3 Runtime operations | Diagnostics, minimal health/readiness, drain/shutdown, trusted proxies/TLS guidance, disk-full and worker backpressure | RUN-01, SEC-01 |
| O7.4 Dashboard acceptance | Settings/backup/audit/recovery, member isolation, offline assets, responsive and keyboard walkthroughs | UI-01, NFR-01 |
| O7.5 Artifacts/upgrades | Linux amd64/arm64 binary smoke and Docker images; signature/provenance/checksum verification, migrations and matching-binary rollback | OPS-02 |
| O7.6 OSS release | MIT license, notices/SBOM, contribution/security/adapter docs, changelog, compatibility results, benchmark record and release checklist | OPS-02, NFR-01–NFR-06 |
| O7.7 Storage boundary | Record local SQLite as the v0.1 authority and move libSQL/Turso certification behind a real driver and service account | DATA-02, OPS-01 |

**Stable gate:** all required PRD acceptance passes on release artifacts. No unresolved bypass, secret exposure, data-loss path, quota race, unsafe retry, or known false compatibility claim. A clean-host restore and upgrade/rollback demonstration is mandatory.

**Blast radius:** entire data directory and deployed users. Snapshot/restore failure must be reproducible on disposable data before attempting operator data.

## Evidence — 2026-09-15

- Live paired snapshots pause the projection worker across both SQLite copies, verify integrity and generation, include the provider master key when needed, and produce authenticated AES-256-GCM archives. Restore rejects the wrong key, truncation, trailing data, unsafe entries, mismatched generations, and newer schemas before atomically publishing an absent target directory.
- Owner-only scheduled local/S3 backup settings, recent-sign-in checks, visible job failures, SHA-256 checksums, retention warnings, S3 SigV4 uploads, bounded retries/timeouts, local archive retention, and cancellation-aware shutdown are covered by focused tests. `/readyz` reports database or projection-capacity failures.
- Portable configuration preview/import/export excludes identities and secrets, validates all references before a transaction, serializes provider changes, and clears a stored credential if an imported connection changes its adapter or endpoint. Retention removes projected event detail and old audits while preserving authoritative accounting and enforcement state.
- Settings and Audit dashboard pages use the typed client. Audit supports actor/action/resource/time filters, stable keyset pagination, safe detail rendering, empty states, and long-ID wrapping. Owner controls are hidden from admins and enforced by the server.
- The release workflow verifies, packages Linux amd64/arm64 archives, publishes checksums, SPDX SBOM and attestations, and builds a multi-architecture container only after release verification succeeds. Docker, systemd, backup/restore, upgrade/rollback, adapter, security, contribution, compatibility, benchmark, and release-checklist documentation is included.
- The local release gate passed Go tests and race checks, 29 frontend/SDK tests, frontend production build, OpenAPI validation, Linux amd64/arm64 builds, embedded-binary smoke, encrypted backup/restore, checksum verification, and desktop plus 360-pixel onboarding/settings/audit walkthroughs. Live cloud certification and publication credentials remain explicit maintainer tasks.
