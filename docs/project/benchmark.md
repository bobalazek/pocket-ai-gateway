# Performance measurements

These are reproducible local measurements of identified artifacts, not certification of a future release tag or live provider. The [PRD](prd.md) defines the startup, idle-memory, streaming-concurrency, and JSON-overhead targets.

## Reproduce

Build the standalone artifact with `./scripts/build.sh`, then run:

```sh
go run ./scripts/benchmark -binary dist/pocket-ai-gateway -duration 60s -stream-duration 5m -cooldown 2m > benchmark.json
```

To measure the native Linux release executable using local Docker:

```sh
./scripts/build.sh
./scripts/cross-build.sh
./scripts/benchmark-linux.sh -duration 60s -stream-duration 5m -cooldown 2m > benchmark-linux.json
```

The wrapper selects the Docker server's native amd64/arm64 architecture and the matching `dist/cross` artifact. A disposable Go toolchain container supplies the separate load generator and `ps`; it runs as a non-root user with read-only source/modules and no external network. Only temporary benchmark data is written. The measured gateway uses the same static build flags as the release packager; neither the load generator nor toolchain becomes part of the deployed application. The Docker toolchain image and Go modules must be cached before a network-isolated run.

The harness requires Go and `ps`. It creates and removes its own temporary data directory and loopback mock provider; it accepts no existing data directory or external provider URL. It does not run GitHub jobs or incur provider charges. `-duration 1s -stream-duration 1s -cooldown 0` is a quick harness check, not a load baseline. Stream duration defaults to the JSON duration when omitted; cooldown defaults to two minutes. The only runtime measurement added is a numeric goroutine count in existing authenticated diagnostics; no profiling endpoint is enabled.

The production services seed one owner, one session, one inference key, one provider connection/public model, synthetic pricing, and an enabled instance daily quota of 1,000,000 requests. The gateway then runs as a separate process from the supplied binary, with normal authorization, durable admission, routing, accounting, background projection, and two SQLite stores. Prompt capture remains off. Migrations finish before timing begins. Startup is measured from process launch through successful `/readyz`, five times; idle RSS is sampled two seconds after the last start.

JSON uses exact 1,024-byte OpenAI-compatible request bodies at a fixed 50 starts/second for the selected duration. The mock waits at least 10 ms and returns token usage. Added latency is each request's complete client-observed response time minus its **measured** mock wait, including any delay in the mock timer. It conservatively includes both loopback transports, gateway work, and harness scheduling; it is not a difference between unrelated percentiles. Scheduling lag and achieved rate are recorded so an overloaded load generator cannot silently lower the requested load.

The subsequent stream workload keeps 50 workers issuing complete SSE requests for the selected stream duration. Each stream has a 10-ms initial mock delay, 20 text chunks 100 ms apart, usage, and a terminal event. First-response timing ends at the first complete SSE line. End-to-end stream duration naturally includes approximately 1.9 seconds of mock generation time. A management diagnostics request and external RSS/CPU sample run every 200 ms throughout both workloads. Pending usage projection counts are sampled there and must drain within 30 seconds after load. All responses must contain the matching mock request ID and usage; streams must finish with `[DONE]`; authoritative usage must account for every request with no unknown attempts.

The JSON output contains the artifact checksum/build metadata, source revision/worktree state, machine details, percentile/count/error metrics, sampled resource/projection peaks, and database/WAL sizes. Stream resource measurements are grouped into minute windows with RSS/goroutine minima and maxima. After closing its idle client connections, the harness waits through cooldown and records final RSS/goroutines. Missing measurements fail the run rather than reporting zero. `response_ready` means the entire body for JSON and the first complete line for SSE. `ps` CPU is the host's process CPU statistic, not a hardware-independent instantaneous utilization measurement. Sampled RSS and goroutine counts establish only the observed duration and workload, not an unlimited-time guarantee. No separate admission-only microbenchmark, large-history scenario, slow consumer, TLS proxy, or translation matrix is implied.

## Initial desktop baseline — September 22, 2026

Command: `go run ./scripts/benchmark -duration 60s > /tmp/pocket-gateway-benchmark-20260922.json`. Started at 12:41:56 UTC and completed successfully. The final harness changes bound shutdown to ten seconds and rename the ambiguous `first_response_line` output field to `response_ready`; they do not change the load or timing calculation.

| Environment | Recorded value |
| --- | --- |
| Binary SHA-256 | `e7bddb9b17bebc3175dd68b21caa36c2ec4372abd57984cdd977a22bfd20712d` |
| Binary VCS revision | `a4ab1a272198d3b67de084197c6c74108225c79e`, build reports `vcs.modified=true` |
| Artifact scope | Local development binary built before the subsequent self-update readiness fix; not a tagged final release artifact |
| Build/runtime | Go 1.27.1; `darwin/arm64`; `-trimpath`; `CGO_ENABLED=1`; SQLite 3.53.4 through `modernc.org/sqlite` 1.58.0 |
| Host | Apple M1 Max, 10 logical CPUs, 32 GiB RAM; macOS 26.6.2 (25G83), Darwin 25.6.0 |
| Storage | Local APFS volume `/dev/disk3s5`; 926 GiB capacity, 245 GiB available; default temporary-directory location on that volume |
| Isolation | Disposable stores and loopback mock; ordinary developer machine, not an otherwise idle dedicated performance host |
| Request mix | 100% OpenAI-compatible text on the `openai_compatible` adapter; JSON followed by SSE; no translation, prompt capture, or gateway response cache |

All times below are milliseconds, with nearest-rank percentiles.

| Measurement | Count | p50 | p95 | p99 | Maximum | Errors |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Existing-instance launch to readiness | 5 | 25.20 | 38.81 | 38.81 | 38.81 | 0 |
| JSON end-to-end | 3,000 | 15.52 | 23.92 | 59.84 | 166.06 | 0 |
| JSON added gateway/transport time | 3,000 | 4.67 | 12.63 | 48.46 | 155.65 | 0 |
| JSON scheduled-start lag | 3,000 | 0.48 | 1.01 | 6.54 | 46.68 | — |
| SSE time to first complete line | 1,550 | 22.65 | 98.06 | 259.23 | 387.90 | 0 |
| SSE added time before first line | 1,550 | 12.38 | 87.62 | 248.00 | 373.02 | 0 |
| SSE complete response | 1,550 | 1,943.62 | 2,051.50 | 2,219.97 | 2,343.49 | 0 |
| Management diagnostics during both workloads | 605 | 0.89 | 5.83 | 22.62 | 190.35 | 0 |
| Management diagnostics during streams only | 305 | 1.02 | 8.31 | 23.48 | 190.35 | 0 |

The JSON workload achieved 49.975 requests/second including completion of its final requests, with eight maximum in-flight client requests. The stream workload reached 50 simultaneous mock requests and ran for 61.128 seconds including final stream completion. All 4,550 inference requests and attempts settled, with 1,164,800 input tokens, 34,000 output tokens, and zero unknown attempts. The $1.2328 recorded cost is synthetic fixture accounting, not actual spend.

Initial idle RSS was **27.36 MiB**. Across 605 samples, loaded RSS peaked at **194.64 MiB** and `ps` process CPU at **66.1%**. Pending usage projection peaked at **100 events**, then reached zero within **917.52 ms** after load. The initial idle measurement does not claim that post-load RSS immediately returns to that value.

| File | Before process start (bytes) | After load, process still running (bytes) |
| --- | ---: | ---: |
| `system.db` | 765,952 | 8,761,344 |
| `system.db-wal` | absent | 4,239,512 |
| `system.db-shm` | absent | 32,768 |
| `data.db` | 36,864 | 5,140,480 |
| `data.db-wal` | absent | 4,148,872 |
| `data.db-shm` | absent | 32,768 |

| PRD target | What this run establishes |
| --- | --- |
| NFR-02: existing instance starts within 2 s | Met on this host: slowest of five starts was 38.81 ms; migrations excluded. |
| NFR-03: idle RSS below 100 MiB | Met for the initial small configured instance: 27.36 MiB. |
| NFR-04: 50 streams, bounded resources, responsive management | Fifty simultaneous complete streams and responsive management demonstrated. Sampled peak RSS is recorded; bounded goroutine counts and long-duration resource stability remain unmeasured. |
| NFR-05: p95 added overhead below 20 ms at 50 requests/s with 1-KiB bodies | Met in this workload: 12.63 ms, with all 3,000 scheduled requests completed. The p99 tail was 48.46 ms. |

This initial run predates goroutine measurement and the subsequent Linux stability checks. Its artifact and results are retained for comparison; they do not establish Linux release behavior.

## Linux before/after — September 22, 2026

Both runs used `./scripts/benchmark-linux.sh -duration 60s -stream-duration 5m -cooldown 2m`: one minute of 50 JSON requests/second, five minutes with 50 stream workers, then two minutes of cooldown. Both completed successfully with zero inference or management errors and every inference attempt accounted for. The first run exposed per-request transports retaining unused connections; [the fix and regression evidence](learnings/2026-09-22-upstream-transport-lifetime.md) explain the change.

| Environment/artifact | Before connection cleanup | After connection cleanup |
| --- | --- | --- |
| Started (UTC) | 14:02:55 | 14:23:46 |
| Linux arm64 SHA-256 | `0e82d6975c384227ab547d6ec7bbcaf23eff1f43aff8303c9496afac5a8296d1` | `f773c21913d7d2d39b4c1a43bf97106c119bc5b80c1fb741be17fd5362cecc07` |
| Binary build metadata | `a4ab1a272198d3b67de084197c6c74108225c79e`, modified worktree | Same base revision, modified worktree including the connection fix |
| Build | Go 1.27.1, Linux arm64/v8.0, CGO disabled, trimpath, stripped release build flags | Same |
| Runtime | Linux 7.0.12-linuxkit on local Docker; 10 vCPUs; 8,319,504,384 bytes guest RAM | Same |
| Storage | Disposable container overlay, 32 GiB capacity, 3.9 GiB available | Same storage class, 3.5 GiB available |

These are identified development artifacts, not a published tag. Source fixes are recorded in commits `804b504` (connection lifetime) and `c6c7417` (diagnostics); the measured files retain their original build metadata. The machine was not a dedicated performance host. Other test work ran during cooldown, after load sampling ended; no builds or test suites ran during the measured JSON/SSE workload. Measurements concern the gateway process, excluding the separate load generator.

| Measurement | Before | After |
| --- | ---: | ---: |
| JSON responses / errors | 3,000 / 0 | 3,000 / 0 |
| Complete streams / errors | 7,728 / 0 | 7,714 / 0 |
| Total settled requests / unknown attempts | 10,728 / 0 | 10,714 / 0 |
| Observed simultaneous upstream streams | 50 | 50 |
| Achieved JSON requests/second | 50.002 | 50.003 |
| Slowest of five launches (ms) | 28.20 | 30.38 |
| Initial idle RSS (MiB) | 20.62 | 20.64 |
| Peak sampled RSS (MiB) | 194.40 | 51.82 |
| Initial / peak / post-cooldown goroutines | 15 / 7,715 / 14 | 15 / 273 / 13 |
| Post-cooldown RSS (MiB) | 156.45 | 44.62 |
| JSON added time p95 / p99 (ms) | 11.65 / 22.47 | 12.93 / 51.88 |
| JSON end-to-end p95 (ms) | 22.18 | 23.37 |
| SSE first complete line p95 (ms) | 49.15 | 53.46 |
| Management during streams p95 (ms) | 8.55 | 6.79 |
| Pending projection peak / drain time (ms) | 143 / 629.01 | 126 / 203.23 |

The fix reduces retained resources; this comparison does not claim improved response latency. The final run includes 2,742,784 input tokens and 157,280 output tokens, all settled at a synthetic $3.057344 fixture price. No provider was charged.

| Full stream minute | Before RSS range (MiB) | Before goroutine range | After RSS range (MiB) | After goroutine range |
| --- | ---: | ---: | ---: | ---: |
| 1 | 135.96–194.40 | 6,099–7,715 | 36.25–49.86 | 129–244 |
| 2 | 171.92–191.74 | 4,790–6,215 | 49.11–50.29 | 202–218 |
| 3 | 164.70–172.23 | 4,814–4,897 | 49.57–51.82 | 193–273 |
| 4 | 163.16–164.71 | 4,813–4,869 | 50.14–50.99 | 201–217 |
| 5 | 162.45–163.20 | 4,810–4,870 | 50.21–51.08 | 203–218 |

Each full minute contains 300 samples; a short sixth window covers completion of the final streams. The corrected process settled around 50–52 MiB during sustained streaming and returned to 13 goroutines after cooldown. It meets the measured NFR-02/03/05 thresholds and demonstrates NFR-04 concurrency, responsiveness, and stable resources for this five-minute workload. Repeat the measurement on the exact tagged artifact and deployment hardware before making broader production-capacity or longer-duration claims. The harness's one-second rehearsal also passed race detection after adding the sampler/window logic.

## Merged release candidate — September 24, 2026

The untagged Linux arm64 binary packaged from clean `master` revision `0eda21731d6849cfa00088c04443b58d47b39874` has SHA-256 `c2b18f5eb19f16536b27f96bb40d93b01eacffd3b2ef26be9cf554fbe43e5383`. The benchmark harness measured that packaged binary directly with `-duration 60s -stream-duration 5m -cooldown 2m` in a network-isolated Linux arm64 Docker container on an Apple M1 Max host (Go 1.27.1, 10 guest CPUs, 8.32 GB guest RAM, tmpfs test storage). The binary reports `vcs.modified=false`. This is candidate evidence, not a tagged release or a live-provider test.

| Measurement | Result |
| --- | ---: |
| Slowest of five starts | 40.32 ms |
| Initial idle RSS | 21.33 MiB |
| JSON requests / errors at 50 starts per second | 3,000 / 0 |
| Achieved JSON rate; added gateway/transport p95 | 50.0004 requests/s; 8.27 ms |
| Complete streams / errors over five minutes | 7,703 / 0 |
| Peak concurrent upstream streams; management p95 during streams | 50; 7.33 ms |
| Sampled peak RSS; peak goroutines | 53.08 MiB; 232 |
| Post-cooldown RSS; goroutines | 48.95 MiB; 15 |
| Pending usage projection drain | 304.32 ms |

All 10,703 inference requests and attempts settled with zero unknown attempts. The $3.054088 recorded cost uses synthetic pricing; no provider was charged. The five full stream minutes peaked at 50.15, 51.72, 50.61, 50.95, and 52.91 MiB respectively. This run meets the measured startup, idle-memory, 50-stream, and p95 JSON-overhead targets on this host and workload. The local Docker disk was full, so the disposable benchmark used tmpfs for its data rather than the usual container overlay; deployment storage and a future tagged artifact require separate verification.
