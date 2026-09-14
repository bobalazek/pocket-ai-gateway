# 2026-09-14 — Local observability, catalog updates, and backups

ID: ADR-006 · Status: accepted · Source: owner's follow-up

**Context.** The owner wants rich usage/logging, no public telemetry, an updatable free-model catalog, scheduled backups, S3 storage, and recovery.

**Decision.** Store detailed redacted operational events locally by default. No product telemetry is sent publicly. Raw prompts/responses/tool arguments remain an explicit bounded capture option. Catalog refresh may fetch versioned data from this project's GitHub releases/repository when enabled; it never uploads user/request data.

Provide local and S3-compatible encrypted backups, scheduled nightly when the operator enables/configures a schedule, with visible last success/failure, retention, retries, and restore verification. Defaults create no remote account, use no S3 credentials, and perform no remote write.

Catalog updates are bounded data downloads with schema/source/version checks; never executable plugins. Discovery does not silently publish routes or broaden grants. Free-model prices require provenance and can expire. Signed release artifacts are the upgrade trust boundary.

**Consequences.** S3 capability is part of v0.1 operations, not deferred. Local startup remains independent of network services. The dashboard distinguishes provider calls, opt-in catalog checks, and backup uploads from telemetry.
