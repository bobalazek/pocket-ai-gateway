# 2026-09-14 — Next.js dashboard and Go APIs

ID: ADR-002 · Status: accepted · Source: owner's preference and approval of the Phase 1 plan

**Context.** The owner prefers Next.js over Vite/Astro and mentions possible SSR or Next.js API processing. Request-time Next.js server features conflict with a Go-only runtime.

**Decision.** Use Next.js App Router with TypeScript for the dashboard. In v0.1, use output: export, basePath /_, trailingSlash enabled, browser-side data fetching, and all dynamic management/inference APIs in Go. No server actions, request-time SSR, or Next.js API backend in this mode.

Use prebuilt screen routes with query-string IDs for database records, such as /_/requests/?id=opaque-id. Do not require generateStaticParams for runtime-created users/keys/models. Go serves the exported HTML/assets and returns proper dashboard/API 404s.

**Consequences.** Next.js remains familiar to contributors and can later gain an optional server deployment if requested. A static export cannot provide dynamic server functions; keep this distinction explicit. Runtime clarification remains in human-tasks; the Go foundation is independently implementable.

Source: [Next.js static exports](https://nextjs.org/docs/app/guides/static-exports).
