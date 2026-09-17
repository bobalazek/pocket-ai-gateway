# Phase 9 — Realtime and extensible media

Status: complete for the current release. [Plan index](README.md)

| Work package | Completion evidence | Boundary |
| --- | --- | --- |
| Realtime | Authenticated native OpenAI HTTP/1.1 WebSocket relay, public-model rewriting, egress controls, 16 MiB frames, 10-minute sessions, and terminal token settlement | No WebRTC, SIP, Live-session credentials, or cross-provider translation |
| Speech streaming | Raw and SSE OpenAI-compatible speech passthrough with provider voice names and a 64 MiB response ceiling | No retry or fallback after dispatch |
| Asynchronous media | Durable create/list/retrieve/cancel API, encrypted content, bounded poll retries, explicit unknown-dispatch restart recovery, usage admission, and dashboard status | Provider-specific input/output remains JSON |
| Provider drivers | Replicate prediction create/poll/cancel and Together video create/poll/local cancel | Live paid-provider certification requires operator credentials |
| Custom adapters | Revisioned embedded Goja request/response transforms, configuration portability, audit records, editor, time/size/path/header limits, and media-job normalization | Trusted owner/admin synchronous JSON only; no script filesystem, process, module, timer, or direct network access; no hard per-invocation heap ceiling |

Completion requires the full local verification suite, copied-binary smoke test, and Docker backup/restore journey. Deterministic local upstreams cover provider contracts without claiming a paid live result.
