# Project

Pocket AI Gateway is the final product name. It is an MIT-licensed, self-hosted gateway with one Go server, a Next.js dashboard, users/application keys, provider routing, local operational history, and configurable limits.

**Current state:** the reviewed single-process gateway, dashboard, identity, keys, limits, accounting, native and translated inference, routing, provider catalog, audit, backup/restore, and release tooling are runnable. Broader provider presets are being certified; unsupported stateful and media APIs remain explicit.

- [PRD](prd.md): intended product and acceptance.
- [Decisions](decisions/README.md): dated context → decision → consequences records.
- [Implementation plan](../plan/README.md): phase files, dependencies, tasks, and evidence.
- [Human tasks](human-tasks.md): only unresolved decisions or external actions.
- [Testing strategy](testing.md): unit, integration, end-to-end, browser, and compatibility expectations.
- [Learnings](learnings/README.md): observed discoveries as implementation proceeds.
- [Incidents](incidents/README.md): actual failures and recovery records once the product runs.

Update current-state descriptions when behavior ships. Do not claim a planned feature is implemented. New decisions supersede old records through a linked entry; they do not erase history.
