# Phase 7 — Operations, security, and v0.1 release

Status: planned; no implementation evidence yet. [Plan index](README.md)

Depends on beta. Some operational groundwork begins in phase 1; this phase completes it for distribution.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| O7.1 Backup/restore | Paired encrypted archives, scheduled local/S3-compatible jobs, upload/checksum/retry/retention status, integrity checks, secret-version consistency, offline staged restore and recovery-mode reconciliation | OPS-01, SEC-01 |
| O7.2 Configuration/retention | Transactional preview/import/export, bounded metadata/capture/aggregate/audit retention, no enforcement-counter loss | OPS-01, DATA-01 |
| O7.3 Runtime operations | Diagnostics, minimal health/readiness, drain/shutdown, trusted proxies/TLS guidance, disk-full and worker backpressure | RUN-01, SEC-01 |
| O7.4 Dashboard acceptance | Settings/backup/audit/recovery, member isolation, offline assets, responsive and keyboard walkthroughs | UI-01, NFR-01 |
| O7.5 Artifacts/upgrades | Linux/macOS/Windows binary smoke; Linux multiarch Docker; signature/provenance/checksum verification, migrations and matching-binary rollback | OPS-02 |
| O7.6 OSS release | MIT license, notices/SBOM, contribution/security/adapter docs, changelog, compatibility results, benchmark record and release checklist | OPS-02, NFR-01–NFR-06 |
| O7.7 Remote storage | Certify libSQL/Turso backend transactions, migrations, unknown commits, single-writer lease, paired export/restore, and no stale-local fallback | DATA-02, OPS-01 |

**Stable gate:** all required PRD acceptance passes on release artifacts. No unresolved bypass, secret exposure, data-loss path, quota race, unsafe retry, or known false compatibility claim. A clean-host restore and upgrade/rollback demonstration is mandatory.

**Blast radius:** entire data directory and deployed users. Snapshot/restore failure must be reproducible on disposable data before attempting operator data.
