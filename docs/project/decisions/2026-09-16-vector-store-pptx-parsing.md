# 2026-09-16 — Bounded PPTX parsing for Vector Stores

ID: ADR-046 · Status: accepted · Source: delegated Phase 8 compatibility scope extending ADR-041, ADR-042, and ADR-045

**Context.** Vector Store content and search can already derive UTF-8 and DOCX text without persisting a second plaintext copy. PPTX uses the same bounded ZIP/XML standard-library primitives, but slide filename order is not the presentation order and package relationships are an input trust boundary.

**Decision.** For `.pptx` attachments, open at most 1,024 ZIP entries, require unique presentation and relationship parts, follow internal slide relationships in the order declared by `ppt/presentation.xml`, and reject external, duplicate, missing, or package-traversing slide references. Cap presentation metadata, total declared slide XML, and derived slide text at 16 MiB. Accept strict or transitional PresentationML/DrawingML namespaces and preserve slide text, tabs, line breaks, and paragraphs through the existing 64 KiB chunks and backend lexical search.

**Consequences.** Content retrieval and search work for bounded PPTX slide text without another dependency or stored plaintext representation. Speaker notes, comments, charts without text nodes, embedded media/OCR, animations, PDF, legacy Office, and spreadsheets remain unsupported until they have separate bounded contracts.
