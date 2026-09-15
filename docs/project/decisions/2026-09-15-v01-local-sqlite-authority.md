# 2026-09-15 — v0.1 local SQLite authority

ID: ADR-014 · Status: accepted technical scope · Source: delegated implementation decision refining ADR-003

**Context.** The requested default is local SQLite with optional remote libSQL/Turso compatibility. The current official Go choices have different tradeoffs. `go-libsql` requires CGO and currently publishes native libraries only for Linux and macOS, which conflicts with the portable Windows build. Turso's recommended `tursogo` local/cloud-sync driver is newer and changes transaction, sync, and recovery behavior. Two independently synchronized databases cannot safely authorize spend without a tested paired-commit and restore protocol.

**Decision.** The v0.1 runtime uses the bundled SQLite engine and one local writer for `system.db` and `data.db`. It does not advertise a remote libSQL or Turso backend. Remote certification moves to the provider/API expansion phase and must cover both stores, a single-writer lease, atomic admission, network loss during commit, migrations, snapshot generations, credential recovery, and clean-host restore.

**Consequences.** SQL syntax compatibility alone is not treated as transaction or recovery compatibility. There is no automatic remote-to-local fallback that could admit requests from stale grants, limits, or prices. Encrypted S3-compatible backups provide off-host durability while local SQLite remains authoritative. This narrows ADR-003's delivery timing without reversing its remote compatibility target.
