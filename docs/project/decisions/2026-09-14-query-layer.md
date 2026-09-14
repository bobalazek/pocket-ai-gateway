# 2026-09-14 — Explicit SQL with generated Go queries

ID: ADR-004 · Status: selected · Source: owner delegated the database tooling choice

**Context.** GORM is a viable Go ORM; this gateway depends heavily on precise multi-policy transactions, two store boundaries, and auditable migrations. An ORM is not required for Go best practice.

**Decision.** Use database/sql and sqlc-generated typed queries, with versioned reviewed SQL migrations per store. Use a maintained local SQLite driver and separately certified remote drivers. Do not introduce GORM or automatic schema synchronization into this initial implementation.

**Consequences.** SQL remains explicit at the enforcement boundary while sqlc removes repeated scanning/type boilerplate. Generated output is checked for drift. SQL features must pass both local and supported remote transaction/query contract tests; the driver is not abstracted behind a speculative generic repository framework.

Sources: [sqlc](https://sqlc.dev/), [GORM](https://gorm.io/docs/).
