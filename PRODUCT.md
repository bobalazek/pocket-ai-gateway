# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Go 1.27 runtime with an embedded static Next.js and TypeScript dashboard. Local SQLite is the default store; remote libSQL/Turso support is planned as an explicit deployment choice.

## Users

Self-hosting operators administer an AI gateway for themselves or a team. Owners and administrators configure providers, models, users, keys, policies, and operations. Members use only the models, keys, request history, and usage their grants allow.

## Product Purpose

Pocket AI Gateway gives operators one local-first service for exposing OpenAI-, Anthropic-, and Gemini-compatible APIs while centralizing upstream credentials, access control, limits, routing, usage, and recovery.

## Positioning

It applies the PocketBase deployment model to an AI gateway: one executable, one data directory, an embedded administration dashboard, and useful defaults without a required hosted control plane.

## Operating Context

Operators run the executable directly or in Docker, open the dashboard on a loopback-bound server, connect upstream providers, publish stable model names, issue scoped API keys, and inspect requests and usage. Automated clients use a protocol-specific base path; operators use the separate management API and dashboard.

## Capabilities and Constraints

- Product name: Pocket AI Gateway.
- Open-source under the MIT license; no hosted edition or billing is planned now.
- Multiple administrators and members are implemented in Phase 2.
- Protocol namespaces remain separate under `/api/openai`, `/api/anthropic`, and `/api/gemini`; management resources live under `/api/v1`.
- The runtime defaults to two local databases, `system.db` and `data.db`, inside one protected data directory.
- The shipped artifact is a single Go executable. Docker packages that same runtime.
- Public telemetry is disabled. Operational telemetry stays local.
- Full protocol compatibility is a measured, endpoint-by-endpoint goal; unfinished surfaces must remain documented as unfinished.

## Brand Commitments

The product name and PocketBase-inspired local deployment model are fixed. Product copy should be direct, calm, and operational. The dashboard uses a precise black-and-white base with a restrained electric-blue accent; green is reserved for semantic success states.

## Evidence on Hand

The repository contains the approved PRD, architecture, protocol, dashboard, operations, and phased delivery documents. There are no production metrics, customer claims, testimonials, or provider certification results yet; future work must not fabricate them.

## Product Principles

- Keep deployment local, inspectable, and reversible.
- Make protocol behavior explicit at the URL and preserve native semantics.
- Apply authorization, limits, routing, and accounting consistently to every inference request.
- Prefer safe defaults and honest capability reporting over implicit fallback.
- Preserve operator privacy and keep secrets out of logs and exports.

## Accessibility & Inclusion

The dashboard targets keyboard operation, visible focus, semantic structure, readable contrast, reduced motion, 200% zoom, and core administration from 360-pixel mobile layouts through desktop. Phase reviews record which of these checks have passed.
