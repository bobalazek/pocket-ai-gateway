# 2026-09-15 — Gateway-owned Conversations and attached streams

ID: ADR-016 · Status: accepted · Source: owner's explicit full OpenAI compatibility requirement; ownership and stream-commit details selected under delegated technical authority

**Context.** OpenAI Responses can attach a Conversation even when `store:false` disables the stored Response resource. Provider-owned Conversation IDs would bind a client to one target and make fallback ownership ambiguous. Incremental delivery can expose bytes before local history commits, so a later storage conflict could leave the client and gateway with different Conversation state.

**Decision.** Pocket AI Gateway owns Conversation IDs, items, and retention under the creating API key. Attached synchronous and background Responses commit new input and successful output atomically after checking the prepared Conversation revision. Attached streams require `store:false`, buffer at most 16 MiB, and replay only after a successful terminal event is validated and the turn commits. A provider `response.failed` or `error` terminal is replayed without changing the Conversation.

**Consequences.** `store:false` means no stored Response; it does not make an attached Conversation stateless. Buffering trades incremental delivery for consistent local history. Provider usage from failed terminal streams is still accounted when present. This record supersedes ADR-015 only where ADR-015 said all streaming remained stateless and Conversations were future work.
