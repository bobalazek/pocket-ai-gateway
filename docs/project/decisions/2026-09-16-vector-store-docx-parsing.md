# 2026-09-16 — Bounded DOCX parsing for Vector Stores

ID: ADR-045 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-041 and ADR-042

**Context.** Vector Store content and search initially accepted only UTF-8 source files. DOCX is a common supported document format and can be parsed safely with Go's ZIP and XML standard libraries, while PDF and legacy binary Office formats require maintained parsers and broader extraction contracts.

**Decision.** For `.docx` attachments, open at most 1,024 ZIP entries, require one `word/document.xml`, cap its declared and extracted body text at 16 MiB, parse strict transitional or strict WordprocessingML, and preserve body text, tabs, line breaks, paragraphs, and table text. Feed the derived text through the existing 64 KiB content chunks and backend lexical search. Decrypt and derive it only on request; persist no extracted plaintext.

**Consequences.** Content retrieval and search work for bounded DOCX body content without a dependency or new stored representation. Corrupt archives, duplicate/missing main documents, oversized XML, and invalid XML fail explicitly. Headers, footers, comments, tracked-deletion semantics, embedded media/OCR, PDF, legacy Office, presentations, and spreadsheets remain unsupported until each has a bounded maintained parser contract.
