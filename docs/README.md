# Project documentation

Updated September 22, 2026. Phase files separate recorded implementation evidence from intended product behavior.

| Document | Purpose |
| --- | --- |
| [PRD](project/prd.md) | Product intent, requirements, release boundaries, and acceptance criteria |
| [Decisions](project/decisions/README.md) | Confirmed constraints, recommended defaults, and unresolved decisions |
| [Architecture](architecture/README.md) | Runtime, module ownership, request flow, concurrency, and dependencies |
| [Data model](architecture/data-model.md) | Persistence, ownership, state transitions, accounting, and retention |
| [API contract](reference/api.md) | Client dialects, compatibility limits, management API, and provider coverage |
| [Management OpenAPI](reference/openapi.yaml) | Machine-readable management API surface |
| [Feature contracts](features/README.md) | Separate OpenAI, Anthropic, Gemini, and auth ownership with example schemas |
| [Dashboard](design/dashboard.md) | Roles, screens, flows, structural wireframes, and responsive behavior |
| [Design system](../DESIGN.md) | Implemented dashboard tokens, shell, components, and interaction rules |
| [Operations](guides/operations.md) | Security, deployment, backup/restore, maintenance, and upgrades |
| [Deployment](guides/deployment.md) | Standalone build, Docker, systemd, TLS proxy, data volume, and upgrade examples |
| [Local demo](guides/demo.md) | Disposable synthetic data, local provider mocks, and dashboard screenshots |
| [Provider adapters](guides/adapters.md) | Rules and evidence required for new upstream adapters |
| [Compatibility](project/compatibility.md) | Tested client operations and explicit boundaries |
| [API parity inventory](project/api-parity-inventory.md) | Implemented, rejected, and pending official API surfaces |
| [Provider certification](project/provider-certification.md) | Preset evidence, cloud authentication boundaries, and live-test status |
| [Release checklist](project/release-checklist.md) | Artifact, restore, provenance, and platform release gate |
| [Testing](project/testing.md) | Unit, integration, end-to-end, browser, and compatibility test policy |
| [Performance baseline](project/benchmark.md) | Measured artifact/environment, load, latency, and resource limits |
| [Operator skill](../skills/pocket-ai-gateway-ops/SKILL.md) | Agent guidance for health, usage, and recovery questions |
| [Roadmap](plan/README.md) | Backend phases, matching frontend work, dependencies, and release gates |
| [First implementation task](plan/tasks/phase-1-foundation.md) | Bounded starting task and its verification contract |
| [Human tasks](project/human-tasks.md) | Remaining user decisions and release prerequisites |

Read PRD → decisions → architecture → plan first. Use the relevant contract before implementing a feature. The PRD owns scope, dated decision records own architectural choices, the data model owns table structure, and the API contract owns wire behavior. Resolve contradictions with a new dated decision rather than rewriting history.

The layout follows the nearby personal SaaS template's docs/project conventions: dated context → decision → consequences records, a separate human-task list, and project learnings/incidents. This project additionally uses docs/plan for phase specifications and implementation tasks. Its Go/runtime conventions are specific to this project; the template's SaaS infrastructure is not copied.

The supplied AI-Gateway-PRD.md was treated as reference material. Its single-administrator scope and narrower limits were superseded by the user's request for users, broader quotas, and routing strategies. The original file in Downloads was left untouched. There is no second, competing PRD in this repository.

Phases 1–9 implement the documented compatibility subset. Final publication gates remain in the release checklist. Additional vendor API families and remote libSQL/Turso implementation are deferred; live provider and off-host S3 service certification require separate evidence.
