# Benchmark record

No production performance claim is published yet.

Before a tagged release, record the exact commit, binary checksum, OS/architecture, CPU, memory, storage, Go version, SQLite version, request mix, concurrency, duration, provider-mock latency, and database size. Measure gateway overhead separately from provider time for JSON, SSE first byte, admission contention, and usage projection backlog. Include p50, p95, p99, error rate, and resource peaks.

The release gate is correctness under bounded concurrency. Throughput targets will be set only after a reproducible baseline exists.
