# 2026-09-15 — Gateway-owned Responses state

ID: ADR-015 · Status: accepted · Source: owner's explicit synchronous and asynchronous response requirement; ownership details selected under delegated technical authority

**Context.** OpenAI Responses defaults to stored state and exposes retrieval, deletion, and background lifecycle endpoints. Forwarding provider-owned response IDs would bind clients to one upstream, leak provider retention choices into the gateway contract, and make fallback ownership ambiguous.

**Decision.** Pocket AI Gateway owns stored Response IDs and state. A non-streaming request is stored for 30 days unless `store:false` is explicit. The gateway always sends `store:false` upstream, assigns a local response ID, and restricts retrieval and deletion to the creating logical API key. Background Responses will use the same local resource contract, durable SQLite state, bounded workers, cancellation, and conservative unknown outcomes after ambiguous dispatch interruption.

**Consequences.** Stored content is included in encrypted backups and local data protection duties. Streaming stays stateless until a complete event-replay contract exists. Provider response IDs are internal provenance rather than public resource IDs. Conversations, prior-response chains, hosted tools, files, and batches remain separate capability and ownership work.
