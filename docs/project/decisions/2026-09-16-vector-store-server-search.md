# 2026-09-16 — Backend-owned Vector Store text search

ID: ADR-042 · Status: accepted · Source: user requirement that search and business logic remain in the backend, plus delegated Phase 8 compatibility scope

**Context.** OpenAI exposes `POST /vector_stores/{vector_store_id}/search`, but gateway-owned stores do not yet have a configured embedding model or an immutable vector-space contract. Returning invented semantic scores or filtering downloaded content in the browser would violate the project's compatibility and frontend boundaries. [OpenAI Vector Store search reference](https://developers.openai.com/api/reference/python/resources/vector_stores/methods/search)

**Decision.** Add bounded server-side search over the parsed UTF-8 chunks from ADR-041. Normalize Unicode letters and numbers, rank chunks by term-frequency cosine similarity from 0 through 1, apply nested `and`/`or` attribute filters and comparison operators in the backend, return at most 50 deterministic results, and refresh the store's activity expiry only after a successful search. Accept query arrays, thresholds, and explicit `ranker: none`; reject query rewriting and embedding-backed ranker names as unsupported. Serialize decryption through the existing bounded File transfer gate and return no stored plaintext.

**Consequences.** The pinned OpenAI SDK search method works with same-key isolation without provider calls, hidden spend, or client-side filtering. This is lexical relevance, not embedding-backed semantic similarity. A later semantic implementation must choose and persist an embedding model/version/dimension contract, charge ingestion and queries through ordinary accounting, and reindex explicitly rather than silently changing scores.
