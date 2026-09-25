# Changelog

All notable changes will be documented here. The project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and intends to use semantic versioning after the first tagged release.

## Unreleased

### Added

- Initial single-binary gateway with an embedded dashboard.
- OpenAI, Anthropic, and Gemini client namespaces with native forwarding and tested cross-provider translation.
- Users, scoped API keys, limits, usage accounting, pricing, provider routing, catalog refresh, audit, diagnostics, and encrypted backup recovery.
- S3 backups preserve object prefixes and reserved characters in object keys.
- System One typed decisions at `/api/systemone/v1` with TypeSafe (Jev) and self-hosted Laya presets, the `decisions:generate` scope, and shared limits and accounting.
- OpenAPI schemas for model lists and embeddings, a System One OpenAPI document, and a client skill for agents.

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
