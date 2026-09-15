# 2026-09-14 — Dashboard palette, icons, and browser API client

ID: ADR-011 · Status: accepted · Source: owner's Phase 1 design follow-up

**Context.** The initial dark-green dashboard did not fit the intended product identity. Mobile navigation needs conventional iconography, and dashboard data access needs one reliable boundary rather than direct component requests.

**Decision.** Use a black-and-white dashboard base with a restrained electric-blue accent. Reserve green for semantic success. Use the configured Lucide icon set for interface actions, including menu and close states.

All browser requests go through the `pocketAIGatewayAdmin` facade. Its namespaces (`auth`, `audit`, `keys`, `models`, `providers`, and the other dashboard features) delegate to feature-local clients built on one shared transport. Hooks use the facade; components and pages never call feature clients or `fetch` directly. The verification gate enforces these boundaries.

**Consequences.** Semantic tokens in `DESIGN.md` and Tailwind/shadcn components carry the visual identity across screens. The API client starts small in Phase 1 and gains generated management types after the Phase 2 OpenAPI contract stabilizes.
