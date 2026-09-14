# Auth feature

Root: /api/v1/auth · Backend: internal/features/auth · Frontend: web/src/features/auth · Requirement: IAM-01, SEC-01

Phase 2 implements owner bootstrap, activation/recovery, login/logout, password and profile changes, session listing/revocation, persistent login throttling, CSRF, origin checks, and offline owner recovery. User administration and application keys remain separate features.

| Operation | Behavior |
| --- | --- |
| GET /setup/status under auth root | Implemented: return only whether owner setup is required or a short same-owner recovery retry remains available; never expose setup codes |
| POST /setup/claim under auth root | Implemented: one-time local setup action creates the sole owner atomically; safe replay with the same short-lived code, identity, and password replaces the setup session if its response/cookie was lost |
| GET /session | Implemented: return safe fields for the current server-side session |
| POST /activate | Implemented: consume a 24-hour activation/recovery code, set an Argon2id password, revoke earlier sessions, and sign in |
| POST /login | Implemented: use a uniform error, persistent failure throttle, bounded password hashing, and a fresh server-side session |
| POST /logout | Implemented: CSRF-protected current-session revocation and cookie clearing |
| GET /sessions | Implemented: own unexpired sessions only |
| DELETE /sessions/{id} | Implemented: CSRF-protected owned-session revocation |
| POST /password | Implemented: verify the current password, change the hash, revoke all sessions, and create one replacement session |
| PATCH /api/v1/me | Implemented: update name; email changes require the current password and revoke other sessions |
| Offline owner reset | Implemented: `owner-reset` requires the exclusive data-directory lock, revokes owner sessions, and writes a protected one-time file |

Login input:

~~~json
{"email":"operator@example.com","password":"example-only"}
~~~

Response contains safe user fields and CSRF/session metadata where needed; session secrets are in HttpOnly cookies, never browser localStorage. Passwords, setup/activation codes, and verifiers are never returned by read APIs.

User lifecycle controls and recovery-code issuance are under /api/v1/admin/users. Admin/member authorization is checked on the server. An invalid login does not disclose whether a user exists. No default password, public signup, or required SMTP service.

Tests: double setup claim, invalid/expired activation, login throttling, fixation/rotation, CSRF and origin rejection, logout, session expiry, password reset/suspension revocation, last-owner guard, multiple admins, and member isolation. Explicitly prove a browser session or management token cannot call inference, and an inference key cannot manage users/settings.

UX: an unclaimed first visit routes to owner setup. Signed-out users route to login, while activation and recovery codes use `/_/activate/`. The persistent account entry opens profile, password, session, and logout controls. Credentials stay in server cookies or form memory and are never written to browser storage.
