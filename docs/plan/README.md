# Implementation phases

Status: Phases 1 through 9 are complete. Optional live-provider and remote-database certification remain explicitly deferred.

## Build order

Build the shared enforcement path before real provider traffic. Add a matching dashboard slice with each backend capability; do not leave the entire frontend until the end.

~~~mermaid
flowchart LR
    P1[1 Runtime and storage] --> P2[2 Users and keys]
    P2 --> P3[3 Limits and accounting]
    P3 --> P4[4 Native gateway]
    P4 --> P5[5 Compatibility]
    P5 --> Alpha[Usable alpha]
    Alpha --> P6[6 Routing and catalog]
    P6 --> Beta[Feature-complete beta]
    Beta --> P7[7 Operations and release]
    P7 --> Stable[v0.1 stable]
    Stable --> P8[8 Provider expansion and updates]
    P8 --> P9[9 Realtime, Live, and extensible media]
~~~

Effort bands are relative implementation sizes, not calendar promises: S is a bounded feature, M spans a few related behaviors, L is a substantial integration, XL must be split further before execution. Confidence reflects known contract risk, not whether work is complete.

| Phase | Backend outcome | Matching frontend outcome | Size / confidence | External dependency |
| --- | --- | --- | --- | --- |
| 1 | Runnable Go/Next.js export, two local stores, migrations, locking, paired snapshot proof | Embedded shell, local status/error page | M / high except platform probes | Supported Go/SQLite toolchain |
| 2 | Secure owner/admin/member accounts and scoped keys | Setup, account, Users, Keys | L / medium | None for the documented role model |
| 3 | Configurable windows/buckets, admission, reservations, delayed repricing | Effective policies and usage provenance | L / medium | No live provider needed |
| 4 | Native OpenAI/Anthropic/Gemini features and provider/model configuration | Providers, Models, request history, basic playground | L / medium | Optional test credentials |
| 5 | All three protocol families, tools/streams, and translated Responses | Compatibility/error explanations and request detail | XL / medium-low | Pinned SDKs, cost-limited provider tests |
| 6 | Provider presets, catalog, all routing strategies | Route preview, strategy controls, catalog/price refresh | L / medium | Provider certification evidence |
| 7 | Local SQLite authority, local/S3 recovery, Docker/binaries, security | Settings, backups, audit, recovery guidance | XL / medium | Signing/release setup and security contact |
| 8 | Broader providers, remaining API parity, optional safe self-update | New presets and explicit update flow | XL / low until probes | Cloud accounts/auth flows and platform evidence |
| 9 | Realtime/Live transports, image-edit SSE, persistent media jobs, Replicate/Together/Gemini drivers, trusted JavaScript transforms | Media-job operations and custom-adapter editor | L / medium | Optional live provider credentials |

## Phase specifications

Each phase file contains backend work items, frontend scope, requirements, dependencies, exit tests, and blast radius. Status is authoritative here; update it only with recorded evidence.

| Phase | Specification | Status |
| --- | --- | --- |
| 1 | [Executable, storage, and recovery foundation](phase-01-runtime-storage.md) | Complete |
| 2 | [Auth, users, sessions, grants, and API keys](phase-02-auth-users-keys.md) | Complete |
| 3 | [Admission, buckets, quotas, and spend accounting](phase-03-limits-usage-pricing.md) | Complete |
| 4 | [First native gateway](phase-04-native-protocol-features.md) | Complete |
| 5 | [Compatibility, translation, and usable alpha](phase-05-cross-protocol-compatibility.md) | Complete |
| 6 | [Routing strategies and provider catalog](phase-06-routing-provider-catalog.md) | Complete |
| 7 | [Operations, security, and v0.1 release](phase-07-operations-release.md) | Complete |
| 8 | [Provider expansion, compatibility inventory, and optional self-update](phase-08-provider-api-expansion.md) | Closed; optional expansion deferred |
| 9 | [Realtime and extensible media](phase-09-realtime-extensible-media.md) | Complete; live certification remains optional |

## Requirement coverage

This table assigns each requirement to delivery phases. Phase 1 provides only the recorded foundation slices; a requirement remains open until all of its phases pass.

| Requirement | Implementation phases |
| --- | --- |
| RUN-01 | 1, 4, 7 |
| IAM-01 | 2 |
| KEY-01 | 2, 3 |
| PROV-01 | 4, 6; expanded coverage 8–9 |
| MODEL-01 | 4, 6 |
| API-01 | 4, 5, 9 realtime/media extension |
| API-02 | 4, 5 |
| API-03 | 4, 5 |
| LIMIT-01 | 3 |
| COST-01 | 3, 4, 6 |
| ROUTE-01 | 4 fixed, 6 strategies/fallback |
| DATA-01 | 1–5, 7 |
| DATA-02 | 1 local stores, 3 outbox, 7 local recovery; optional remote certification deferred by ADR-051 |
| COST-02 | 3 historical pricing, 6 catalog price source, 7 retention/recovery |
| UI-01 | 1–9, paired with backend slices |
| SEC-01 | 1–9, applied as each boundary is introduced |
| OPS-01 | 1 groundwork, 3 accounting recovery, 7 complete |
| OPS-02 | 1 build, 7 distribution/manual upgrades, 8 self-update |
| NFR-01 | 4 first journey, 7 observed usability |
| NFR-02 | 1 baseline, 7 release measurement |
| NFR-03 | 1 baseline, 7 release measurement |
| NFR-04 | 4 stream behavior, 6/7 load measurement |
| NFR-05 | 3 durable admission baseline, 6/7 load measurement |
| NFR-06 | 1–7 durability/isolation regression gates |

## Verification strategy

One local command runs formatting, static analysis, automated tests, frontend type checks/build, race checks, and an embedded-binary smoke test. The manual-only GitHub Verify workflow invokes quick mode without race or copied-binary smoke checks; releases run the full gate. Bounded fuzz, vulnerability, and live-provider jobs add release coverage where documented.

| Layer | Evidence required |
| --- | --- |
| Policy/accounting | Fixed-window/bucket selection, historical repricing idempotency, table-driven boundaries, concurrent admission, restart/clock tests, idempotent reconciliation, user/key aggregate limits |
| Persistence | Migration failure, newer schema refusal, lock contention, disk-full, interrupted dispatch and snapshot/restore |
| Protocol | Golden request/response/SSE fixtures, split-event fuzz seeds, all nine family paths plus Responses, namespace/base-URL checks, native + translated tool cycles, pinned official SDKs |
| Network/security | Header stripping, SSRF/redirect/DNS cases, ownership/role negatives, CSRF, revocation during fallback |
| UI | Auth/session/recovery flows, cross-credential rejection, core keyboard/mobile flows, server pagination, secret exposure/storage inspection, stale/session-expired states |
| Release | Actual downloaded/built artifact with no source tree, Docker volume persistence, clean restore and matching-binary rollback |

Normal CI never needs paid providers. Live tests are isolated, explicitly enabled, budgeted, and disabled for untrusted PR secrets. Release support is limited to combinations with recorded evidence.

### Local verification — 2026-09-22

Checked source revision `a4ab1a2` with Go 1.27.1 on macOS arm64 and the production Linux Docker image. The only code change in this pass extends the local race-test timeout to 15 minutes per package; application behavior is unchanged.

- `POCKET_AI_GATEWAY_QUICK_VERIFY=1 ./scripts/verify.sh` passed: formatting, generated-code/OpenAPI/license drift, vet/staticcheck, all Go tests, dashboard and Linux amd64 builds, TypeScript, and all 73 frontend/official-SDK tests across 15 files.
- All 13 packages in the full verification script's race gate passed with `go test -race -timeout 15m`; the gateway package took 627.149 seconds, confirming that the default 10-minute timeout is insufficient. Quick verification, the unchanged complete race package list, and the copied-binary smoke were run separately; no full-gate check was skipped.
- The copied-binary smoke passed onboarding, sessions, key lifecycle, locking, shutdown, snapshots, and encrypted backup/restore. `./scripts/compose-e2e.sh` passed the production image build, onboarding, encrypted paired-store backup, clean-volume restore, login, and session persistence across restart.
- Browser checks passed first-owner setup, dashboard/navigation, models/media-job empty states, profile saving, audit history, settings/status, sign-out, invalid-password rejection, and sign-in. Mobile, tablet, and desktop checks used 360/768/1280 widths; no browser warning/error logs were reported. Test data and containers were disposable.
- No GitHub workflow or billable provider call was triggered. These are deterministic local checks of the documented compatibility subset, not live-provider or remote-database certification.

### Release closeout — 2026-09-22

- Added `scripts/update-e2e.sh`: real signed Linux CLI dry-run/apply and forced rollback after both databases and the master key change, running as an unprivileged user in a disposable network-isolated container. Linux arm64 passed. This exposed and fixed a production bug where the candidate inherited the public domain and rejected its loopback readiness request; [recorded learning](../project/learnings/2026-09-22-update-readiness-origin.md). The updater race tests also passed, and the bounded independent review found no issues.
- Added a [disposable demo and screenshots](../guides/demo.md) with three synthetic users/providers/models/keys and 124 requests over seven days. Seed, sign-in, accounting/projection totals, operator-data isolation, and cleanup are tested. Populated data exposed a zero-sized usage chart; explicit chart dimensions fixed it, verified at 360/768/1280 widths.
- Recorded the [local performance baseline](../project/benchmark.md): 3,000 JSON requests at 49.975 requests/second, 1,550 complete streams reaching 50 concurrent upstream requests, and 605 management reads, with zero errors and all 4,550 inference requests settled. JSON added p95 was 12.63 ms, initial idle RSS 27.36 MiB, startup maximum 38.81 ms, and management p95 during streaming 8.31 ms. Exact artifact/host and sampled loaded memory are recorded; Linux release measurements, goroutine counts, and long-duration stability remain explicit gates. The short harness race check and vet passed.
- The [release checklist](../project/release-checklist.md) now separates local evidence, final artifact/publication gates, real-service certification, and future API/remote-storage implementation. The operator skill and `llms.txt` already existed; they are linked from the documentation index rather than duplicated.
- Final checks passed on the uncommitted `codex/local-verification` worktree based on `a4ab1a2`: the complete quick verification gate (format/generated-code/OpenAPI/notices, vet/staticcheck, all Go tests, embedded dashboard and Linux amd64 builds, TypeScript, 73 frontend/SDK tests), the rebuilt copied-binary smoke, and the rebuilt production Docker Compose backup/restore/restart journey. The demo race test passed in 11.594 seconds; the changed updater's race suite passed separately, while the unchanged full race package set is recorded in the preceding pass. Independent updater and demo/chart reviews found no actionable issues. Changed Markdown file links and diff whitespace checks passed. No GitHub jobs, real provider calls, commits, or pushes were made.

### Linux resource checks — 2026-09-22

- Existing owner/admin diagnostics now return a sampled `goroutines` count, with matching OpenAPI/TypeScript contracts and anonymous-access rejection coverage. No public profiling endpoint or runtime dependency was added.
- The extended Linux benchmark exposed unused per-request transport pools: the baseline reached 7,715 goroutines, including roughly 4,800 during sustained streaming. Both inference dispatch and media-job transports now release completed connections. Regression tests failed on the original code and pass after the fix for HTTP/1.1, HTTP/2, stream cancellation, and media polling. The [transport-lifetime learning](../project/learnings/2026-09-22-upstream-transport-lifetime.md) also records why the cancellation mock must read its POST body before streaming.
- The complete local quick gate passed after the fix, including all Go tests, format/static analysis, build/schema/notices checks, and all 73 frontend/SDK tests. Focused gateway transport/translation/audio/image/cancellation race tests passed in 27.308 seconds; the complete operations and media-job race suites passed in 36.241 and 37.646 seconds respectively.
- The rebuilt standalone smoke, production Docker Compose backup/restore/restart journey, and signed Linux CLI update/rollback test all passed. The update E2E took 22.163 seconds on Linux arm64. Independent reviews of diagnostics, transport behavior, media polling, and cancellation-test cleanup found no unresolved issues. No GitHub workflow or billable provider was invoked.
- The final [Linux before/after measurement](../project/benchmark.md#linux-beforeafter--september-22-2026) passed 3,000 JSON requests and 7,714 complete streams with zero errors/unknown usage. At 50 concurrent streams, peak goroutines fell from 7,715 to 273 and sampled peak RSS from 194.40 to 51.82 MiB; the fixed process returned to 13 goroutines after cooldown. JSON added p95 was 12.93 ms at 50.003 requests/second, initial idle RSS 20.64 MiB, startup maximum 30.38 ms, and management p95 during streams 6.79 ms. The benchmark sampler's short race rehearsal also passed. The release checklist now records the completed five-minute Linux measurement while retaining exact-tag and deployment-hardware verification as publication gates.

### S3 interoperability — 2026-09-22

- The optional `./scripts/compose-e2e.sh --s3` production-image test exposed an upload failure for prefixes containing `+`; a focused regression also caught changed object keys from path normalization. The shared signer now preserves key paths and applies the S3 encoding rules; [recorded learning](../project/learnings/2026-09-22-s3-object-key-signing.md).
- The rebuilt Linux Docker image passed real local S3 upload, independent download, SHA-256/size comparison, rejected-credential failure reporting, clean-volume restore, login, and restart persistence. The original local-backup Compose path also passed. The historical MinIO fixture is disposable and carries no production dependency or cloud-service certification claim.
- The complete local quick verification gate passed (format, generated/OpenAPI/notices checks, static analysis, all Go tests, dashboard/Linux build, TypeScript, and 73 frontend/SDK tests). The full operations race suite passed in 37.359 seconds. Independent review found no actionable issues. No GitHub workflow or billable service was invoked.

## Working method and next action

### Public documentation and demo review — 2026-09-24

The README was reduced to a Docker quick start, example request, feature summary, SDK base URLs, source build, and documentation links. The disposable demo was rebuilt and signed into locally; its overview, usage chart, and model-routing views were captured as real PNGs. `pnpm --dir web typecheck`, all 78 frontend/SDK tests, `go test ./scripts/demo -count=1`, `docker compose config --quiet`, Markdown link checks, and `git diff --check` passed. Deployment instructions were corrected for service-account creation and for passing the backup key to offline backup/restore. No GitHub workflow or paid provider was used.

Implement one bounded work item at a time in this repository. Update its status and evidence after checks pass. Review financial/security/data-loss boundaries before declaring the containing phase complete. Do not generate all future directories, tables, interfaces, or placeholder endpoints upfront.

The v0.1 implementation phases are complete, with the documented Phase 8–9 compatibility subset implemented. Before publication, select the final candidate, verify its Linux artifacts and sustained resource behavior, and configure signing/reporting/publication access. The [release checklist](../project/release-checklist.md) tracks those gates; [human tasks](../project/human-tasks.md) track external prerequisites. Additional vendor API families and remote storage remain separate implementation work in the [API parity inventory](../project/api-parity-inventory.md) and decisions.
