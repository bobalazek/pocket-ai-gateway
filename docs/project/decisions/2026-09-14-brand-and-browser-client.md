# 2026-09-14 — Dashboard palette, icons, and browser API client

ID: ADR-011 · Status: accepted · Source: owner's Phase 1 design follow-up

**Context.** The initial dark-green dashboard did not fit the intended product identity. Mobile navigation needs conventional iconography, and dashboard data access needs one reliable boundary rather than direct component requests.

**Decision.** Use a black-and-white dashboard base with a restrained electric-blue accent. Reserve green for semantic success. Use the configured Lucide icon set for interface actions, including menu and close states.

All browser requests go through a typed API client or a feature client built on it. Centralize same-origin credentials, CSRF/auth headers, cancellation, safe error parsing, caching, and retry policy. Components do not call `fetch` directly; the verification gate enforces that boundary.

**Consequences.** Semantic tokens in `DESIGN.md` and Tailwind/shadcn components carry the visual identity across screens. The API client starts small in Phase 1 and gains generated management types after the Phase 2 OpenAPI contract stabilizes.
