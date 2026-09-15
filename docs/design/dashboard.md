# Dashboard contract and structural wireframes

Scope: the embedded browser dashboard, including member and administration views. This is a structural design contract; final colors/components and observed usability evidence are a phase 1/2 deliverable.

## Surfaces and navigation

| Surface | Support | Layout |
| --- | --- | --- |
| Desktop web, ≥1024 px | Full | Persistent navigation, bounded wide content, tables and detail views |
| Tablet web, 768–1023 px | Full | Collapsible navigation; fewer summary columns; forms remain readable |
| Mobile web, 360–767 px | Full core administration | Menu drawer, single-column details, full-height edit dialogs, explicit actions |
| Native desktop/mobile/tablet apps | Out of scope | The executable serves a web interface |
| Public marketing site | Out of scope for gateway runtime | Repository documentation is the initial public entry point |
| Public instance entry | Setup/login/activation only | No open registration or public provider directory |

Owner/admin navigation: Overview, Users, Providers, Models, API keys, Requests, Usage, Audit, Settings. Members: Overview, Models, My keys, My requests, My usage, Account. Playground is a contextual action on an eligible model/key. No workspace switcher or billing subscription screen.

User menu contains identity/role, account/security/sessions, local help/about/version, and sign out. Privileged recovery and destructive settings stay on explicit settings pages, not inside an incidental menu.

Every page has a clear title, primary action, scoped filters when needed, and a recoverable state. New/edit forms are dialogs with labelled sections/tabs; long model/key policy forms can use a full-height dialog. Closing warns about unsaved changes but does not trap the user.

## Screen inventory

All routes below are under /_. List/detail views use the same route hierarchy; create/edit occurs in a dialog.

| Screen/route | Job and composition | Actions/data | Critical states |
| --- | --- | --- | --- |
| /setup | Claim → owner → provider → model → key → first request | Setup code, password, provider wizard; retention explanation | Claimed already, expired code, DB failure; resume after login |
| /login and /activate | Authenticate/choose initial password | Session APIs; code in POST body | Invalid/expired/throttled; local recovery instructions |
| /overview | Understand health and recent activity | Request/failure counts, known/estimated spend, unresolved usage, recent requests | No traffic, partial data, upstream unavailable, recovery mode |
| /users/ and /users/?id=… | Manage who can access what | Member list, status, grants, aggregate policies, activation/suspension | Last-owner guard, own-role limits, activation pending |
| /providers/ and /providers/?id=… | Configure an upstream account | Name, adapter, endpoint, masked credential state, capabilities, tests/discovery | Missing secret, invalid URL, unavailable, billable-test confirmation |
| /models/ and /models/?id=… | Publish a stable client model | Catalog selection/manual entry, targets, strategy, capability evidence, price source | Unpublished, stale/free price, no eligible target, breaking embedding change |
| /keys/ and /keys/?id=… | Issue least-privilege credentials | Owner, scopes/models/connections, effective limits, expire/rotate/revoke | Empty grants deny, inherited limit ceiling, one-time secret, lost secret |
| /requests/ and /requests/?id=… | Explain a client operation | Filters, metadata, attempt timeline, route reasons, usage, tool counts; content opt-in | No results, pending, failed, incomplete, unknown cost, content disabled/expired |
| /usage | Understand consumption and limits | Period selector, user/key/model/provider filters, known vs estimated/unknown, remaining caps, original/restated pricing and repricing preview | Stale aggregates, cap exhausted, adjustment pending, no traffic |
| /playground | Test an ordinary authorized request | Public model, client protocol namespace, operation, selected inference key, prompt/tool editor, streaming output | Missing permission/key, price warning, cancel/incomplete/error |
| /audit | Inspect privileged changes | Actor/action/resource/time filters and redacted details | Empty, retention reached, permission denied |
| /settings | Operate the instance | Retention/capture, local/S3 backup jobs, import/export, storage backend/lag, security/proxy summary, version/update guide | Backup failed, import conflict, external secret missing, recovery mode |
| /account | Manage personal security | Display name/password, active sessions, sign out others | Reauthentication, session expired, password validation |

Management API ownership is defined in [API contract](../reference/api.md). Navigation hiding is for clarity; the server rejects unauthorized deep links and requests independently.

## Structural wireframes

These sketches describe hierarchy and states, not final pixels.

### W1: Desktop shell and resource list

~~~text
┌ Navigation ───┬ Page title                         [Primary action] ┐
│ Overview     │ Scope / filters / date range / search              │
│ Users        │ [Inline actionable error or status, when present]   │
│ Providers    │ Name       State       Key fact          Actions    │
│ Models       │ … bounded, server-paginated rows …                  │
│ API keys     │                              [Previous] [Next]      │
│ Requests     │                                                      │
│ Usage        │ Empty: explain why, then offer one relevant action │
│ Settings     │                                                      │
│ Account      │                                                      │
└──────────────┴──────────────────────────────────────────────────────┘
~~~

Users, providers, models, and keys use this pattern. Details open through a normal deep link; editing is explicit. Tables show useful comparison fields first, not every stored field. Filters persist in URL query parameters, without secrets.

### W2: Request detail and usage uncertainty

~~~text
Requests / request ID           [Incomplete]        timestamp
User / key prefix / public model / client protocol
Known usage | Estimated cost | Unresolved reservation
Attempts
  1  primary       429 rejected       no output     timing / cost state
  2  fallback      stream interrupted             timing / cost state
Why this route? [eligible targets / rejected reasons — privileged]
Tools: count and completion status
Content: Capture was disabled for this request.
~~~

Outcome appears before raw diagnostics. Cost badges distinguish reported, estimated, and unknown with text, not color alone. Partial tool arguments are displayed only with content permission/capture; no UI implies the gateway executed a tool.

### W3: Provider/model/key edit dialog

~~~text
Edit resource                                      [Close]
[Basics] [Access or targets] [Limits or advanced]
Field label
[Current editable value]
Help text / validation tied to this field
Effective result: user ceiling ∩ key policy ∩ model capability
Changes that broaden access or change an embedding contract
[Cancel]                                          [Save]
~~~

Simple defaults come first; egress rules, capture, and adaptive routing remain explicit advanced controls. Provider credentials use replace/remove actions and masked status, never a reveal button. Key creation ends in a one-time secret dialog with copy/client configuration; closing explains recovery by rotation.

### W4: Mobile/tablet

~~~text
[Menu]  Page title                           [Account]
[Primary action]
[Filters]
Name / status
Most useful fact
[View details]
…
[Previous] [Next]
~~~

Tablet uses a collapsible rail and compact tables where readable. Mobile uses stacked records for routine management; request timelines stack vertically; code/error panes may scroll horizontally within a labelled region. Advanced forms remain available in full-height dialogs with visible save/cancel. No hover-only action.

### W5: Setup and recovery

~~~text
Pocket AI Gateway
Step 1 of 5: Claim this instance
[Setup code]
[Continue]
Help: obtain the code from the local server terminal.

Recovery mode:
Restored snapshot: timestamp
Inference is paused pending key/usage reconciliation.
[Review keys] [Review unresolved usage] [Owner recovery checklist]
~~~

Owner setup is a resumable sequence. Provider failure leaves the owner logged in and configuration editable. Recovery mode cannot be dismissed by an ordinary member or inferred healthy from a green process-liveness indicator.

## Core flows and verification signals

| Flow | Actor/entry/goal | Decisions and recovery | Observable end state |
| --- | --- | --- | --- |
| First request | Owner, setup URL, configure gateway | Invalid code → terminal recovery; provider failure → edit; missing model → manual registration | A real scoped key completes a request and history shows it |
| Add member | Owner/admin, Users, grant bounded access | Activation expired → regenerate; role/grant escalation → reject | Member logs in and sees only own allowed resources |
| Issue/rotate key | Member or privileged operator, Keys | Requested scopes exceed ceiling → inline error; secret response lost → revoke/rotate | New secret works with unchanged logical quota state |
| Handle limit denial | Member/operator, failed request/Usage | Show active policy, remaining allowance, period/retry time; unknown reservation → privileged reconciliation | User knows whether to wait, reduce request, or ask admin |
| Change route | Owner/admin, Model, switch targets/strategy | Preview permissions/features/cost; stale revision → reload; embedding change → explicit new version | New requests use revised config; old attempts retain snapshots |
| Inspect tool failure | Authorized user, Requests | Tool count visible; content absent/expired has clear reason | Timeline distinguishes provider/tool output from caller execution |
| Backup/recover | Owner/local operator, Settings/CLI | Backup failure → retained previous archive; restore gap → recovery mode | Verified clean restore before admission is re-enabled |

## Interaction and visual foundation

Use a calm, compact operational interface: readable tables and forms, strong page titles, restrained color. No decorative hero on authenticated pages.

Define semantic background/surface/text/muted/border/accent/success/warning/danger/focus tokens before styling. Use a system font stack, monospace for IDs/code, tabular numerals for usage, and a small 4-pixel spacing scale. Light theme first; dark theme only after both contrast variants are checked.

Shared primitives: labelled form field, button, dialog/drawer, status badge, bounded data table/list, filter/pagination controls, one-time secret panel, usage amount with provenance, confirmation panel. Share them after real reuse; charts remain secondary to accessible numbers/tables.

States on every relevant screen: loading, empty, no results, saving, saved, validation error, permission denial, session expired, service error, stale revision, offline, and partial/unresolved data. Preserve non-secret edits on retry. Do not automatically retry mutations that issue credentials or spend money.

Accessibility: visible keyboard focus, proper headings/labels/table semantics, dialog focus trapping/restoration, Escape/cancel, ≥44-pixel touch actions where practical, 200% zoom, readable contrast, reduced motion, and text labels for statuses. Announce stream completion/errors without reading every token aloud.

Before shipping a screen, perform keyboard-only and 360/768/1280-pixel walkthroughs. Verify setup, key issuance, revocation, limit-denial diagnosis, and backup discovery with a new operator. The 2026-09-15 release pass covered first-run setup, sign-in, Settings, Audit, desktop, and 360-pixel layout; it found and fixed a nullable empty-list crash and long-identifier wrapping. The wider key/limit journey remains part of the tagged-release checklist.

The embedded About/Help view explains local data storage, capture/retention, upstream data forwarding, software version/license, and operator responsibility. No hosted billing, tracking cookie banner, or invented legal claims are added to the local app; public project policies belong in repository documentation.
