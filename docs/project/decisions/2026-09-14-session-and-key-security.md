# 2026-09-14 — Session and API-key security

ID: ADR-013 · Status: accepted implementation decision · Source: delegated technical choice

**Context.** Phase 2 adds password authentication, browser mutations, multiple roles, network deployment, recovery, and inference credentials before providers exist.

**Decision.** Passwords use Argon2id with a two-operation memory bound. Browser sessions are server-side, strict SameSite, HttpOnly cookies with a separate double-submit CSRF cookie. A configured public origin pins Host and Origin and decides secure-cookie behavior; forwarded headers are ignored. Non-loopback listeners require that origin. Owner recovery is offline and lock-protected. API keys keep a stable logical ID while secrets use a public selector plus SHA-256 verifier; secret plaintext is returned only at creation/rotation. Empty scope, model, or connection grants deny access.

**Consequences.** Reverse proxies must preserve the configured public Host. Password or sensitive identity changes bump an authentication revision and revoke affected sessions immediately. Provider/model IDs are stored as bounded explicit grant strings until their resource tables arrive; the Phase 4 migration may normalize them without changing key identity.
