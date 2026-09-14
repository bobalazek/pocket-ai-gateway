# Phase 2 — Auth, users, sessions, grants, and API keys

Status: complete. [Plan index](README.md)

Depends on phase 1 storage and verification. Use the owner/admin/member matrix from the PRD.

| Work item | Implementation and acceptance | Requirements |
| --- | --- | --- |
| I2.1 Complete authentication | Build on Phase 1 owner setup and session lookup. Add login/logout/activation, session management, password verification, throttling, CSRF tokens, trusted-host/proxy handling, and offline recovery; no public signup | IAM-01, SEC-01 |
| I2.2 User lifecycle | Admin-created members, owner-created admins, suspension, password recovery, owner transfer, audit; last owner invariant survives concurrent changes | IAM-01, DATA-01 |
| I2.3 Key lifecycle/grants | Stable key plus rotated secrets, deny-by-default scopes/model/connection grants, expiry/revocation; member cannot escalate and rotation retains identity | KEY-01, IAM-01 |
| I2.4 Management contract | First OpenAPI/client slice, safe errors/revisions/pagination, scoped queries; two-member negative access tests cover lists, details, counts, and mutations | UI-01, SEC-01 |
| I2.5 Account settings | Persistent bottom-sidebar identity menu and `/_/account/` page for email, password, active sessions and logout; reauthentication and session revocation protect sensitive changes | IAM-01, UI-01, SEC-01 |

Frontend: login/activation, Account/sessions, Users, and Keys with one-time secret and effective-grant explanation. Extend the existing sidebar and owner identity with role-aware access. Include session-expired, no-grants, and role-denied states.

**Exit gate:** the first browser visit reaches setup when no owner exists; refresh/resume is safe; concurrent owner claim yields one owner; members cannot access each other's data; suspension/revocation blocks authorization immediately; profile/password changes revoke the intended sessions; secrets remain absent from logs/read endpoints/browser storage. Cover these journeys with unit and SQLite/HTTP integration tests plus desktop/mobile browser walkthroughs; add automated browser tests once inference journeys make their runtime cost worthwhile.

**Blast radius:** every management and inference operation. Review isolation with an independent reviewer before live shared use.

## Implementation evidence

- System migration 0003 adds revisioned users/sessions, one-time activation/recovery verifiers, persistent login throttles, stable API keys with rotated secrets, and redacted audit events.
- Auth endpoints implement login, logout, activation/recovery, account updates, password replacement, and owned-session controls with Argon2id, bounded hashing, strict cookies, CSRF, origin, and configured-host checks.
- User management enforces owner/admin/member scope in SQL and service policy. Owner transfer is transactional; status, recovery, email, password, and role changes revoke affected sessions immediately.
- API keys store only a selector and SHA-256 verifier, return plaintext once, retain logical identity on rotation, and require explicit protocol/model/connection grants.
- The embedded dashboard includes login, activation, Users, API keys, and Account/session pages through the shared typed API client.
- `owner-reset` works only while the data directory lock is available and writes its one-time code to an owner-only file rather than output.
- Unit, SQLite integration, HTTP CSRF/origin, copied-binary smoke, desktop/mobile browser, race, cross-build, OpenAPI, and independent review gates pass.
