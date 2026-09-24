# Decisions

One dated file per material product/architecture choice, following the personal SaaS template. Records are append-only once accepted. A reversal gets a new linked record; update this index to show the current ruling.

Each record states status/source, context, decision, and consequences. A recommended implementation detail is not presented as an explicit user decision.

| ID | Current decision | Status |
| --- | --- | --- |
| ADR-001 | [Product, MIT, users, and deployment](2026-09-14-product-and-runtime.md) | Accepted user decisions; owner/member details are implementation defaults |
| ADR-002 | [Next.js dashboard and Go API boundary](2026-09-14-dashboard-runtime.md) | Accepted |
| ADR-003 | [Two databases and optional remote storage](2026-09-14-storage-boundaries.md) | Two stores/local default/remote capability accepted; transaction design selected |
| ADR-004 | [Go database access](2026-09-14-query-layer.md) | Technical choice made under delegated authority |
| ADR-005 | [Key policies, pricing, and usage history](2026-09-14-usage-and-pricing.md) | Configurable controls and delayed pricing accepted; enforcement semantics selected |
| ADR-006 | [Privacy, catalog, and backups](2026-09-14-privacy-catalog-backups.md) | Accepted user decisions; opt-in network/retention behavior specified |
| ADR-007 | [Earlier protocol proposal](2026-09-14-protocol-boundaries.md) | Protocol scope superseded by ADR-008 and ADR-015 |
| ADR-008 | [Three client protocols and translation](2026-09-14-three-protocol-compatibility.md) | Accepted |
| ADR-009 | [Feature namespaces and authentication](2026-09-14-feature-api-namespaces.md) | Accepted |
| ADR-010 | [Earlier dashboard onboarding decision](2026-09-14-dashboard-system-onboarding-tests.md) | Onboarding protection superseded by ADR-017 |
| ADR-011 | [Dashboard palette, icons, and browser API client](2026-09-14-brand-and-browser-client.md) | Accepted |
| ADR-012 | [Concurrency and coordination](2026-09-14-concurrency-and-coordination.md) | Accepted direction |
| ADR-013 | [Session and API-key security](2026-09-14-session-and-key-security.md) | Technical choice made under delegated authority |
| ADR-014 | [v0.1 local SQLite authority](2026-09-15-v01-local-sqlite-authority.md) | Technical scope refining ADR-003; remote certification retained |
| ADR-015 | [Gateway-owned Responses state](2026-09-15-response-resources.md) | Stored and asynchronous responses accepted; ownership contract selected |
| ADR-016 | [Gateway-owned Conversations and attached streams](2026-09-15-conversation-resources.md) | Technical choice extending ADR-015 |
| ADR-017 | [Browser-first owner setup](2026-09-15-browser-first-owner-setup.md) | Accepted user decision superseding ADR-010 onboarding protection |
| ADR-018 | [Gateway-owned stored Chat Completions](2026-09-15-stored-chat-completions.md) | Technical choice extending Phase 8 compatibility |
| ADR-019 | [Bounded native OpenAI Responses web search](2026-09-15-native-openai-web-search.md) | Accepted bounded Phase 8 contract |
| ADR-020 | [Bounded native Anthropic basic web search](2026-09-15-native-anthropic-web-search.md) | Accepted bounded Phase 8 contract |
| ADR-021 | [Gateway-owned Anthropic Message Batches](2026-09-15-gateway-owned-anthropic-message-batches.md) | Accepted bounded Phase 8 contract |
| ADR-022 | [Go package boundaries](2026-09-15-go-package-boundaries.md) | Delegated technical choice |
| ADR-023 | [Gateway-owned OpenAI batch files](2026-09-15-gateway-owned-openai-batch-files.md) | Accepted bounded Phase 8 contract |
| ADR-024 | [Gateway-owned OpenAI Responses Batches](2026-09-15-gateway-owned-openai-responses-batches.md) | Accepted bounded Phase 8 contract |
| ADR-025 | [Anthropic streams use the public model alias](2026-09-15-anthropic-stream-public-model.md) | Technical choice superseding only ADR-020's complete-message_start preservation |
| ADR-026 | [Gateway-owned OpenAI multipart Uploads](2026-09-16-gateway-owned-openai-uploads.md) | Accepted bounded Phase 8 contract |
| ADR-027 | [Gateway-owned OpenAI Chat Completions Batches](2026-09-16-gateway-owned-openai-chat-completions-batches.md) | Accepted bounded Phase 8 contract extending ADR-024 |
| ADR-028 | [Gateway-owned OpenAI Embeddings Batches](2026-09-16-gateway-owned-openai-embedding-batches.md) | Accepted bounded Phase 8 contract extending ADR-024 and ADR-027 |
| ADR-029 | [Gateway-owned OpenAI Moderations Batches](2026-09-16-gateway-owned-openai-moderation-batches.md) | Accepted bounded Phase 8 contract extending ADR-024, ADR-027, and ADR-028 |
| ADR-030 | [Gateway-owned OpenAI Image Generation Batches](2026-09-16-gateway-owned-openai-image-generation-batches.md) | Accepted bounded Phase 8 contract extending ADR-024, ADR-027, ADR-028, and ADR-029 |
| ADR-031 | [Gateway-owned OpenAI Image Edit Batches](2026-09-16-gateway-owned-openai-image-edit-batches.md) | Accepted bounded Phase 8 contract extending ADR-024 through ADR-030 |
| ADR-032 | [Bounded native Anthropic basic web fetch](2026-09-16-native-anthropic-web-fetch.md) | Accepted bounded Phase 8 contract |
| ADR-033 | [Native OpenAI legacy Completions and gateway-owned Batches](2026-09-16-native-openai-legacy-completions.md) | Accepted bounded Phase 8 contract extending ADR-024 through ADR-031 |
| ADR-034 | [Backend-owned dashboard metadata](2026-09-16-backend-owned-dashboard-metadata.md) | Accepted user direction extending ADR-002 and ADR-011 |
| ADR-035 | [Native OpenAI image-generation streaming](2026-09-16-native-openai-image-generation-streaming.md) | Accepted bounded Phase 8 contract |
| ADR-036 | [Bounded native Gemini Interactions](2026-09-16-native-gemini-interactions.md) | Accepted bounded Phase 8 contract |
| ADR-037 | [Gateway-owned OpenAI File purposes](2026-09-16-openai-file-purposes.md) | Accepted bounded Phase 8 contract extending ADR-023 |
| ADR-038 | [Rotating provider credential references](2026-09-16-rotating-provider-credentials.md) | Accepted cloud-auth boundary |
| ADR-039 | [Gateway-owned OpenAI Vector Store lifecycle](2026-09-16-gateway-owned-openai-vector-stores.md) | Accepted bounded Phase 8 contract |
| ADR-040 | [Explicit authenticated Linux self-update](2026-09-16-explicit-linux-self-update.md) | Accepted Phase 8 Linux update contract |
| ADR-041 | [Gateway-owned Vector Store text content](2026-09-16-vector-store-text-content.md) | Accepted bounded Phase 8 contract extending ADR-039 |
| ADR-042 | [Backend-owned Vector Store text search](2026-09-16-vector-store-server-search.md) | Accepted bounded Phase 8 contract extending ADR-039 and ADR-041 |
| ADR-043 | [Gateway-owned Vector Store file batches](2026-09-16-gateway-owned-vector-store-file-batches.md) | Accepted bounded Phase 8 contract extending ADR-039 |
| ADR-044 | [Atomic Vector Store creation with Files](2026-09-16-vector-store-create-with-files.md) | Accepted bounded Phase 8 contract extending ADR-039 and ADR-043 |
| ADR-045 | [Bounded DOCX parsing for Vector Stores](2026-09-16-vector-store-docx-parsing.md) | Accepted bounded Phase 8 contract extending ADR-041 and ADR-042 |
| ADR-046 | [Bounded PPTX parsing for Vector Stores](2026-09-16-vector-store-pptx-parsing.md) | Accepted bounded Phase 8 contract extending ADR-041, ADR-042, and ADR-045 |
| ADR-047 | [Bounded UTF-16 text decoding for Vector Stores](2026-09-16-vector-store-utf16-text.md) | Accepted bounded Phase 8 contract extending ADR-041 and ADR-042 |
| ADR-048 | [Bounded HTML parsing for Vector Stores](2026-09-16-vector-store-html-parsing.md) | Accepted bounded Phase 8 contract extending ADR-041 and ADR-042 |
| ADR-049 | [Gateway-owned Responses file search](2026-09-16-gateway-owned-responses-file-search.md) | Accepted bounded Phase 8 contract extending ADR-015, ADR-039, and ADR-042 |
| ADR-050 | [Bounded XLSX parsing for Vector Stores](2026-09-16-vector-store-xlsx-parsing.md) | Accepted bounded Phase 8 contract extending ADR-041, ADR-042, and ADR-046 |
| ADR-051 | [Close v0.1 with local SQLite](2026-09-16-close-v01-with-local-sqlite.md) | Accepted release scope; supersedes ADR-014's Phase 8 timing only |
| ADR-052 | [Asynchronous media and scripted provider adapters](2026-09-17-async-media-and-scripted-adapters.md) | Accepted user decision extending provider expansion |
| ADR-053 | [Native Live transports and Gemini media](2026-09-17-native-live-and-gemini-media.md) | Accepted user decision extending ADR-052 and Phase 9 |
| ADR-054 | [Anthropic dynamic web filtering](2026-09-17-anthropic-dynamic-web-tools.md) | Delegated parity expansion extending ADR-020 and ADR-032 |
| ADR-055 | [Anthropic web-tool response inclusion and cache bypass](2026-09-18-anthropic-web-tool-response-inclusion.md) | Delegated parity expansion extending ADR-020, ADR-032, and ADR-054 |
| ADR-056 | [Anthropic combined web search and fetch](2026-09-18-anthropic-combined-web-tools.md) | Delegated parity expansion extending ADR-020, ADR-032, ADR-054, and ADR-055 |
| ADR-057 | [Anthropic prompt caching with hosted web tools](2026-09-18-anthropic-prompt-cache-web-tools.md) | Delegated parity expansion extending ADR-020, ADR-032, ADR-054, ADR-055, and ADR-056 |
| ADR-058 | [Bounded PDF text parsing for Vector Stores](2026-09-24-vector-store-pdf-parsing.md) | Delegated parity expansion extending ADR-041, ADR-042, and ADR-049 |
| ADR-059 | [Gateway-owned Anthropic Files](2026-09-24-gateway-owned-anthropic-files.md) | Delegated bounded native Files lifecycle and same-key Messages references |
| ADR-060 | [Versioned hosted web-search call pricing](2026-09-24-versioned-web-search-call-pricing.md) | Delegated pricing expansion extending ADR-005 and hosted-search contracts |

Historical: [initial proposal](2026-09-14-initial-proposal.md), superseded by the records above where they differ. Its A/P/D/M identifiers are historical references only; current documents use ADR IDs.

## New-entry template

~~~markdown
# YYYY-MM-DD — Decision title

ID: ADR-NNN · Status: accepted/proposed/superseded · Source: user decision or delegated technical choice

**Context.** What forced the choice and which alternatives mattered.

**Decision.** The selected behavior and scope.

**Consequences.** Implementation obligations, limitations, and what this supersedes.
~~~
