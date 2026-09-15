# 2026-09-15 — Browser-first owner setup

ID: ADR-017 · Status: accepted · Source: user decision

**Context.** Docker on Linux is the default deployment. Requiring operators to retrieve a setup code from a container volume or log adds friction to the first browser visit.

**Decision.** An instance without an active owner accepts the first valid same-origin owner claim from the setup page without a setup code. The database transaction and unique index allow exactly one active owner. Setup-code protection is deferred as an optional opt-in control.

**Consequences.** An unclaimed public instance can be claimed by the first client that reaches the endpoint, so operators should complete setup immediately after deployment or keep ingress restricted until the owner exists. This supersedes ADR-010 only where it required a local setup code.
