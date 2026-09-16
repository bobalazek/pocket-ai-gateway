# 2026-09-16 — Close v0.1 with local SQLite

ID: ADR-051 · Status: accepted release scope · Source: owner's instruction to finish the self-contained baseline and defer optional expansion

**Context.** ADR-003 retains optional remote libSQL/Turso as a product direction, while ADR-014 made local SQLite authoritative for v0.1 and moved remote certification into later expansion. No maintained remote driver, live account, or destructive recovery evidence is present. Treating SQL compatibility as delivered storage support would create a false durability claim.

**Decision.** Close the current implementation phases with local SQLite as the only advertised v0.1 authority. Encrypted local and S3-compatible backup/restore provide the supported recovery paths. Remote libSQL/Turso remains a future work package and must satisfy ADR-014 before it can be advertised.

**Consequences.** This supersedes ADR-014 only where it assigned remote certification to Phase 8; it does not reverse ADR-003's optional long-term target. Phase 8 is closed with that expansion explicitly deferred.
