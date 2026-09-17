# Phase 9 — Realtime and extensible media

Status: complete for the current release. [Plan index](README.md)

| Work package | Completion evidence | Boundary |
| --- | --- | --- |
| Realtime and Live | Authenticated native OpenAI Realtime, OpenAI Live, and Gemini Live HTTP/1.1 WebSocket relays; first-event/query public-model rewriting; egress controls; 16 MiB frames; 10-minute sessions; terminal or cumulative token settlement | No WebRTC, SIP, stored-session credentials/controls, sideband/forks, or cross-provider translation |
| Image edit streaming | Native GPT Image multipart edits with named partial/completed SSE validation and terminal usage settlement | `n=1`, 0–3 partial images, no post-dispatch fallback |
| Speech streaming | Raw and SSE OpenAI-compatible speech passthrough with provider voice names and a 64 MiB response ceiling | No retry or fallback after dispatch |
| Asynchronous media | Durable create/list/retrieve/cancel API, encrypted content, bounded poll retries, explicit unknown-dispatch restart recovery, usage admission, and dashboard status | Provider-specific input/output remains JSON |
| Provider drivers | Replicate prediction create/poll/cancel, Together video create/poll/local cancel, and Gemini Veo `predictLongRunning` create/poll/local cancel | Live paid-provider certification requires operator credentials; deprecated OpenAI Videos compatibility is intentionally omitted |
| Custom adapters | Revisioned embedded Goja request/response transforms, configuration portability, audit records, editor, time/size/path/header limits, and media-job normalization | Trusted owner/admin synchronous JSON only; no script filesystem, process, module, timer, or direct network access; no hard per-invocation heap ceiling |

Completion evidence (2026-09-17): the full local verification suite passed, including static analysis, backend and dashboard tests, race detection, the Linux binary build, and the copied-binary smoke journey. The Docker Compose build, first-run setup, backup, restore, and restart journey also passed. Deterministic local upstreams cover OpenAI Realtime and Live, Gemini Live, GPT Image edit SSE, Replicate, Together, Gemini Veo, and custom adapter contracts without claiming paid-provider certification.
