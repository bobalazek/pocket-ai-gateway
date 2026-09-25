# Changelog

All notable changes will be documented here. The project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and intends to use semantic versioning after the first tagged release.

## Unreleased

### Changed

- On/off settings in the dashboard use switches, selects draw a consistent chevron, read-only fields look read-only, and provider connections show readable labels, a compact base URL, and an enabled switch.
- The dashboard builds with Node.js 26, and CI and release jobs run on Ubuntu 26.04 with Node 24-based actions. The container image installs pnpm with npm because Node.js 26 no longer bundles Corepack.

## [0.1.0-alpha.1] - 2026-09-25

First public alpha. See the warning in the README before relying on it.

### Added

- Initial single-binary gateway with an embedded dashboard.
- OpenAI, Anthropic, and Gemini client namespaces with native forwarding and tested cross-provider translation.
- Users, scoped API keys, limits, usage accounting, pricing, provider routing, catalog refresh, audit, diagnostics, and encrypted backup recovery.
- S3 backups preserve object prefixes and reserved characters in object keys.
- System One typed decisions at `/api/systemone/v1` with TypeSafe (Jev) and self-hosted Laya presets, the `decisions:generate` scope, and shared limits and accounting.
- OpenAPI schemas for model lists and embeddings, a System One OpenAPI document, and a client skill for agents.
- System One presets for Vercel AI Gateway (Jev), Codiv, and self-hosted Kev, CLM, and OpenJev, with error normalization for each server's format.
- Request timing: every request records streaming or synchronous mode and time to first byte; request history shows duration and first byte per request and attempt, and analytics break down p95 duration and time to first byte by model, operation, and response mode.

### Fixed

- Configuration changes no longer wait for slow upstream responses or open Realtime/Live sessions, which could stall all new inference.
- Native OpenAI Chat and Completions streams record provider usage even when the client does not request it.
- Native Anthropic streams that end with an `error` event or without `message_stop`, and native OpenAI Chat streams that report an error chunk or end without `[DONE]`, are recorded as failed instead of succeeded.
- Settlements that fail transiently, for example while a backup holds the database, are retried in the background instead of holding reservations and concurrency leases until restart.
- Shutdown cancels and settles remaining requests, including WebSocket sessions, before closing the stores, and no longer exits with an error when streams are open.
- A connection's `timeout_ms` bounds stream idle gaps instead of total stream length, so long generations are no longer truncated.
- Provider client errors (400, 409, 422, 429) keep their status on the Anthropic, Gemini, and translated paths instead of becoming 502.

### Security

- Only the owner can set `env:` or `file:` provider credential references, which read host environment variables and files.
- Anonymous malformed login attempts no longer trigger an instance-wide dashboard login lockout.
- Provider dial checks also reject shared-address (100.64.0.0/10) and benchmarking (198.18.0.0/15) ranges unless private networking is allowed.
