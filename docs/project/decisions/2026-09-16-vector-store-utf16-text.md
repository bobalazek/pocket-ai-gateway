# 2026-09-16 — Bounded UTF-16 text decoding for Vector Stores

ID: ADR-047 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-041 and ADR-042

**Context.** OpenAI documents ASCII, UTF-8, and UTF-16 as supported text encodings for File Search. ASCII is already valid UTF-8, but the gateway rejected UTF-16 before content retrieval or search.

**Decision.** Decode BOM-marked little- or big-endian UTF-16 with Go's standard library before the existing 64 KiB UTF-8 chunking and lexical-search paths. Reject odd byte counts, unpaired surrogates, decoded NUL characters, and derived text over 16 MiB. Continue to reject UTF-16 without a byte-order mark because the stored File contract does not retain a trustworthy charset parameter.

**Consequences.** Supported text encodings now cover unambiguous ASCII, UTF-8, and UTF-16 input without another dependency or stored plaintext copy. Charset inference and malformed replacement remain intentionally unsupported.
