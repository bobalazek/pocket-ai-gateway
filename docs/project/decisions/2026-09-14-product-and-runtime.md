# 2026-09-14 — Product, license, and runtime

ID: ADR-001 · Status: accepted · Source: owner's follow-up

**Context.** The initial proposal left the name/license provisional and narrowed the source PRD to one administrator.

**Decision.** The product is **Pocket AI Gateway**, licensed MIT. One open-source self-hosted edition; no hosted billing/subscriptions. One Go server/executable by default, with optional Docker packaging. Support multiple admins plus member accounts. Keep a protected owner role for recovery/ownership actions; this does not limit the number of admins.

Select 127.0.0.1:8080 as the default runtime listener and ./pocket_gateway_data as the default data directory. Both are configurable; services use absolute paths. Next.js development can use 127.0.0.1:8081, never a production dependency.

**Consequences.** Supersedes the provisional name, Apache-2.0 recommendation, and source single-admin scope. The repository now carries MIT terms; the copyright notice names project contributors. Platform artifacts must preserve the single-runtime experience.

License text source: [OSI MIT license](https://opensource.org/license/mit).
