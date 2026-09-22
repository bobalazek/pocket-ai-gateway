# 2026-09-22 — Update probes need their own loopback origin

**Context.** Standalone operators set `POCKET_AI_GATEWAY_PUBLIC_URL` to their public HTTPS domain. The updater starts a candidate on a temporary loopback listener and checks `/readyz`.

**Evidence.** The real Linux CLI E2E test initially rolled back a healthy v1.1.0 candidate: the candidate inherited the public domain, so its host check rejected the loopback readiness request until the probe timed out. Unit tests with a simplified health server had not reproduced this boundary.

**Correction.** The temporary probe now passes its own loopback `--public-url`. The deployed configuration is unchanged. The E2E test passes with an external public origin and also proves rollback after a deliberately broken signed candidate modifies both databases and `master.key`.

**Consequence.** Keep the real-executable Linux gate alongside fast updater tests. Testing a probe with a simplified HTTP handler is insufficient to prove it works with production host validation.
