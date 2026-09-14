# Implementation phases

Status: Phases 1 and 2 are complete. Later phases remain planned.

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
| 7 | Remote-store certification, local/S3 recovery, Docker/binaries, security | Settings, backups, audit, recovery guidance | XL / medium | Signing/release setup, security contact, optional remote test accounts |
| 8 | Broader providers, remaining API parity, optional safe self-update | New presets and explicit update flow | XL / low until probes | Cloud accounts/auth flows and platform evidence |

## Phase specifications

Each phase file contains backend work items, frontend scope, requirements, dependencies, exit tests, and blast radius. Status is authoritative here; update it only with recorded evidence.

| Phase | Specification | Status |
| --- | --- | --- |
| 1 | [Executable, storage, and recovery foundation](phase-01-runtime-storage.md) | Complete |
| 2 | [Auth, users, sessions, grants, and API keys](phase-02-auth-users-keys.md) | Complete |
| 3 | [Admission, buckets, quotas, and spend accounting](phase-03-limits-usage-pricing.md) | Planned |
| 4 | [First native gateway](phase-04-native-protocol-features.md) | Planned |
| 5 | [Compatibility, translation, and usable alpha](phase-05-cross-protocol-compatibility.md) | Planned |
| 6 | [Routing strategies and provider catalog](phase-06-routing-provider-catalog.md) | Planned |
| 7 | [Operations, security, and v0.1 release](phase-07-operations-release.md) | Planned |
| 8 | [Broader providers, full API inventory, and optional self-update](phase-08-provider-api-expansion.md) | Planned |

## Requirement coverage

This table assigns each requirement to delivery phases. Phase 1 provides only the recorded foundation slices; a requirement remains open until all of its phases pass.

| Requirement | Implementation phases |
| --- | --- |
| RUN-01 | 1, 4, 7 |
| IAM-01 | 2 |
| KEY-01 | 2, 3 |
| PROV-01 | 4, 6; expanded coverage 8 |
| MODEL-01 | 4, 6 |
| API-01 | 4, 5 |
| API-02 | 4, 5 |
| API-03 | 4, 5 |
| LIMIT-01 | 3 |
| COST-01 | 3, 4, 6 |
| ROUTE-01 | 4 fixed, 6 strategies/fallback |
| DATA-01 | 1–5, 7 |
| DATA-02 | 1 local stores, 3 outbox, 7 remote certification/recovery |
| COST-02 | 3 historical pricing, 6 catalog price source, 7 retention/recovery |
| UI-01 | 1–7, paired with backend slices |
| SEC-01 | 1–7, applied as each boundary is introduced |
| OPS-01 | 1 groundwork, 3 accounting recovery, 7 complete |
| OPS-02 | 1 build, 7 distribution/manual upgrades, 8 self-update |
| NFR-01 | 4 first journey, 7 observed usability |
| NFR-02 | 1 baseline, 7 release measurement |
| NFR-03 | 1 baseline, 7 release measurement |
| NFR-04 | 4 stream behavior, 6/7 load measurement |
| NFR-05 | 3 durable admission baseline, 6/7 load measurement |
| NFR-06 | 1–7 durability/isolation regression gates |

## Verification strategy

One local command runs formatting, static analysis, focused automated tests, frontend type checks/build, and an embedded-binary smoke test. CI invokes the same gate. Supported-platform race/artifact tests and bounded fuzz/vulnerability jobs add release coverage; their scope is documented separately.

| Layer | Evidence required |
| --- | --- |
| Policy/accounting | Fixed-window/bucket selection, historical repricing idempotency, table-driven boundaries, concurrent admission, restart/clock tests, idempotent reconciliation, user/key aggregate limits |
| Persistence | Migration failure, newer schema refusal, lock contention, disk-full, interrupted dispatch and snapshot/restore |
| Protocol | Golden request/response/SSE fixtures, split-event fuzz seeds, all nine family paths plus Responses, namespace/base-URL checks, native + translated tool cycles, pinned official SDKs |
| Network/security | Header stripping, SSRF/redirect/DNS cases, ownership/role negatives, CSRF, revocation during fallback |
| UI | Auth/session/recovery flows, cross-credential rejection, core keyboard/mobile flows, server pagination, secret exposure/storage inspection, stale/session-expired states |
| Release | Actual downloaded/built artifact with no source tree, Docker volume persistence, clean restore and matching-binary rollback |

Normal CI never needs paid providers. Live tests are isolated, explicitly enabled, budgeted, and disabled for untrusted PR secrets. Release support is limited to combinations with recorded evidence.

## Working method and next action

Implement one bounded work item at a time in this repository. Update its status and evidence after checks pass. Review financial/security/data-loss boundaries before declaring the containing phase complete. Do not generate all future directories, tables, interfaces, or placeholder endpoints upfront.

The next action after Phase 2 review is Phase 3 admission, limits, and accounting. Provider traffic still waits for the shared enforcement path.
