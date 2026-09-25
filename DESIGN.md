# Dashboard design system

Pocket AI Gateway uses an embedded, local-first administration dashboard built with Next.js, Tailwind CSS, and source-owned shadcn components. The interface should feel calm, operational, and explicit about what is ready, unavailable, or risky.

## Foundations

- Light mode is the supported first theme. Use the semantic CSS tokens in `web/src/app/styles.css`; do not hardcode new product colors in components. The canvas is cool off-white, with white data surfaces and a dark charcoal navigation rail.
- The system font stack is the default. Use monospace only for identifiers, endpoints, and code; use tabular numerals for usage and cost.
- Spacing follows a 4-pixel base. Corners are restrained, with 8-pixel controls and 10-pixel surfaces. Shadows are reserved for raised primary panels and dialogs.
- Use a precise black-and-white base with a restrained electric-blue accent for actions, links, selection, and focus. Green is reserved for semantic success. Warnings and failures must include text or icons rather than color alone.

## Application shell

Desktop uses a 248-pixel dark sidebar and a wide content column. Page headers are compact so data appears in the first viewport. Keep Overview, Status, Users, Providers, Models, API keys, Requests, Analytics, Usage, Audit, and Settings in stable order according to role. The signed-in account belongs at the bottom and opens email, password, session, and logout controls.

At 900 pixels and below, collapse the sidebar into a compact header with a labelled Lucide menu/close control and native keyboard-accessible disclosure panel. Content becomes a single column, primary actions remain visible, and pages must work at 360, 768, and 1280 pixels without horizontal scrolling.

## Components

Add reusable UI primitives under `web/src/components/ui` through the configured shadcn registry. A single on/off setting uses the `Switch` component, a native checkbox with `role="switch"`; checkboxes remain for choosing several items from a list, and radios for choosing one. Selects use the shared `.select` style with its drawn chevron, and read-only inputs render muted. Prefer semantic server components and add client JavaScript only for real interaction. Use cards for grouped state, tables for comparable records, dialogs for bounded edits, and full pages for multi-step setup.

All browser API calls go through `web/src/lib/api-client.ts` or a feature client built on it. Components do not call `fetch` directly. The client keeps same-origin credentials, CSRF, abort, error, and no-retry behavior consistent.

Overview leads with readiness, period metrics, daily request volume, and recent requests. Analytics compares traffic, known spend, tokens, failure counts, and whole-gateway duration by API key, user, public model, provider connection, protocol, operation, and outcome. Its period and entity filters share the URL with deep links into request history; unknown cost and retained history remain explicit. Usage holds effective limits, price versions, reconciliation, and detailed daily accounting. Charts use the shadcn Chart component with Recharts v3. Every chart needs an accessible layer, a readable text summary or table, explicit units and periods, and non-color-only series labels. Requests use comparable rows; full attempts and cost provenance belong in the detail view.

## Interaction and content

The URL is the source of truth for selected records and filters. Primary page pagination is URL-backed; independent secondary lists use explicit load-more controls. Forms have persistent labels, field-level errors, safe retry behavior, visible focus, and 44-pixel minimum targets. Setup is resumable; destructive or billable actions state their effect before execution.

Never display provider secrets, API key plaintext after the one-time issue step, raw captured request content by default, or health as proof of authorization/provider readiness. The authenticated Status page shows local database and accounting checks separately from public liveness/readiness; it does not imply an upstream provider is available. Planned screens must be marked unavailable until their APIs exist.

Before closing a dashboard phase, run keyboard and 360/768/1280 walkthroughs plus the repository's browser tests. Record fixes in the phase evidence.
