# 2026-09-14 — Dashboard system, onboarding, and test layers

ID: ADR-010 · Status: accepted · Source: owner's Phase 1 follow-up

**Context.** The dashboard needs a consistent component base, stable navigation, useful usage charts, first-visit administration, account settings, agent-readable help, and reviewable tests across phases.

**Decision.** Use Tailwind CSS with source-owned shadcn components. Use shadcn Chart with Recharts v3 when usage data arrives. Keep Overview, Status, Users, Providers, Models, API keys, Requests, Usage, Audit, and Settings in the role-appropriate sidebar, with the signed-in account at the bottom.

An unclaimed instance routes its first dashboard visit into a resumable owner setup flow. A short-lived local setup code creates the owner; no pre-created account or default credential exists. The account page owns email, password, sessions, and logout.

Each phase adds meaningful unit, real-boundary integration, built-artifact end-to-end, and browser tests as its behavior appears. Maintain `/llms.txt` and a repository operator skill so agents can discover implemented endpoints and report health/usage without inventing unavailable statistics.

**Consequences.** Phase 1 establishes the component/tooling shell, first-run owner claim, help surface, operator skill, and shared verification command. Phase 2 completes login, recovery, session, account, user, and key flows. Phase 3 introduces accessible Recharts usage views. Future phase review gates include the matching test layers and preserve explicit planned-versus-implemented states.
