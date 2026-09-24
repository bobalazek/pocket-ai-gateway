# 2026-09-24 — Bounded PDF text parsing for Vector Stores

ID: ADR-058 · Status: accepted · Source: user-requested post-v0.1 improvement extending ADR-041, ADR-042, and ADR-049

**Context.** Gateway-owned Files retain encrypted bytes, but Vector Store content, search, and Responses `file_search` rejected PDF. Go's standard library has no PDF parser. An external renderer or OCR service would break the single-executable deployment; PDF parsing also needs explicit work limits because these bytes are untrusted.

**Decision.** Use the pinned, pure-Go, MIT-licensed `github.com/giraffesyo/pdf` v0.7.0 parser to derive selectable text on request. Require a PDF 1.x/2.x header and the existing 16 MiB File limit. Parse strictly and sequentially under the request context with a 10-second deadline, at most 256 pages, 16 MiB per decoded stream and for extracted text, and explicit per-page operator/glyph, form-depth, and image-count limits. Reuse the existing 64 KiB chunks, key isolation, and encrypted File transfer gate. Persist no parsed plaintext.

**Consequences.** The same parsed PDF text feeds Vector Store content, lexical search, and bounded Responses `file_search` without a second index or provider call. Invalid, incomplete, password-requiring, and image-only PDFs fail explicitly. OCR, embedded images, annotations, forms, and exact visual layout are outside this contract. The parser is newly released and security-sensitive, so its pinned source, license notice, and malformed/compressed-input regression remain part of dependency upkeep.
