# 2026-09-16 — Bounded XLSX parsing for Vector Stores

ID: ADR-050 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-041, ADR-042, and ADR-046

**Context.** Local Vector Store content and search already derive text from bounded OOXML word-processing and presentation packages. XLSX uses the same ZIP/XML trust boundary, but worksheet order comes from workbook relationships and text may be stored in shared-string, inline-string, or cached cell values.

**Decision.** Parse `.xlsx` attachments with Go's standard library. Open at most 1,024 unique ZIP entries; require valid workbook, relationship, and worksheet parts; follow only internal worksheet/shared-string relationships; reject external, duplicate, missing, or package-traversing references; and cap declared XML plus derived text at 16 MiB. Follow workbook sheet order and emit shared, inline, numeric, boolean, and cached formula values as tab-separated rows through the existing 64 KiB content chunks and lexical search.

**Consequences.** Content retrieval, backend search, and Responses file search work for bounded spreadsheet cell values without another dependency or stored plaintext copy. Formulas are not evaluated; cached values are used. Styles, formatted dates, charts, comments, macros, external links, embedded media, and legacy spreadsheet formats remain unsupported.
