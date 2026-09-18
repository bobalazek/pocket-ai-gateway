# Anthropic-compatible feature

Root: /api/anthropic/v1 · SDK base root: /api/anthropic · Backend owner: internal/gateway

Own Messages/count_tokens/models, Anthropic headers/types/errors, content blocks, tool mappings, SSE events, upstream encoding, and fixtures. Authentication validates an inference key; it does not create a browser session.

## Illustrative Messages exchange

POST /api/anthropic/v1/messages, with x-api-key gateway-key and a supported anthropic-version:

~~~json
{"model":"assistant","max_tokens":32,"messages":[{"role":"user","content":"Say hello."}]}
~~~

Response shape:

~~~json
{
  "id":"msg_example",
  "type":"message",
  "role":"assistant",
  "model":"assistant",
  "content":[{"type":"text","text":"Hello."}],
  "stop_reason":"end_turn",
  "stop_sequence":null,
  "usage":{"input_tokens":4,"output_tokens":2}
}
~~~

Errors preserve the Anthropic envelope:

~~~json
{"type":"error","error":{"type":"invalid_request_error","message":"The requested feature is unavailable for this route."},"request_id":"req_example"}
~~~

## Stream and tool contract

The stream follows message_start, ordered content_block_start/delta/stop events, message_delta, and message_stop. The gateway replaces only `message_start.message.model` with the requested public model; the remaining native events retain their provider fields. A tool_use block contains id/name/input; tool_result content links back to the original tool ID. Partial argument JSON is forwarded through input_json_delta, not flattened into text.

Translate ordinary JSON requests to/from OpenAI and Gemini while preserving multiple tool calls/results, stop reasons, usage, refusals, and cancellation. Streaming currently requires an Anthropic-compatible target: Anthropic requires input usage in its first event, while OpenAI and Gemini report it only at completion. Opaque thinking/signatures need an explicit valid mapping/affinity rule; unsupported ones are not silently removed. Count tokens using a capable target-specific path, with honest estimate semantics.

Automated coverage pins the Anthropic SDK and verifies that its `/v1/messages` path is appended exactly once, then exercises OpenAI, Anthropic, and Gemini upstream families, native headers/errors, translated tool cycles, and stream shapes.

## Gateway-owned Message Batches

The six Message Batches operations are available at `POST/GET /api/anthropic/v1/messages/batches`, `GET/DELETE /api/anthropic/v1/messages/batches/{id}`, `POST /api/anthropic/v1/messages/batches/{id}/cancel`, and `GET /api/anthropic/v1/messages/batches/{id}/results`. A submission contains one to four requests and is limited to 16 MiB. Each item has a unique `custom_id` matching `^[a-zA-Z0-9_-]{1,64}$` and a `params` object for an ordinary non-streaming Message with `max_tokens` of at least one.

The creating inference key owns the batch and needs `chat:generate` plus `messages:batches`. Each item executes locally through the ordinary Messages authorization, current grants, model routing, admission, and accounting path. The submission validates the outer batch envelope; invalid nested `params` become asynchronous per-item `errored` results, so one item failing does not prevent the remaining items from completing. This is gateway-owned asynchronous execution; it does not forward an Anthropic provider batch, inherit Anthropic's 100,000-item limit, or receive Anthropic's native 50% batch price.

New batches start `in_progress`. Cancellation moves unfinished work through `canceling`; work already dispatched may finish before the batch reaches `ended`. Until then, `request_counts` keeps every item in `processing` and its terminal counters at zero; the terminal distribution appears together at `ended`. The public `expires_at` is the processing deadline 24 hours after creation, when unfinished items become expired. Results stream as newline-delimited JSON objects shaped as `{ "custom_id": ..., "result": ... }`. Result types are `succeeded`, `errored`, `canceled`, or `expired`; order is not guaranteed, so clients match results through `custom_id`. Only an ended batch can be deleted. At 29 days after creation, the gateway deletes the whole batch and its results; it does not retain archived batch metadata.

## Native web search

JSON and SSE Messages may contain one `{ "type": "web_search_20250305", "name": "web_search" }` basic tool, `{ "type": "web_search_20260209", "name": "web_search" }` dynamic-filtering tool, or `{ "type": "web_search_20260318", "name": "web_search" }` dynamic-filtering tool that also supports response inclusion, alongside ordinary application tools. `max_uses` is required and must be an integer from 1 through 4. The basic version defaults to direct invocation. The dynamic versions default to `code_execution_20260120`; `allowed_callers` may select direct or any supported code-execution caller without duplicates. Optional `strict` must be boolean, and `response_inclusion` is accepted only on `web_search_20260318` with a value of `full` or `excluded`. Requests may provide up to 100 `allowed_domains` or `blocked_domains`, never both, and an approximate `user_location` with at least one location field. Prompt caching and web search cannot be combined in the same request.

The public and upstream model must both publish `chat` and `web_search`; code-execution invocation additionally requires `web_search_dynamic` on both, and the `response_inclusion` field additionally requires `web_search_response_inclusion` on both. The selected target must use the Anthropic adapter and built-in `anthropic` preset. The key needs `chat:generate` and `messages:web_search`. Eligible ordered, weighted, and latency strategies may choose a target before dispatch. The request rejects translation, deferred tool loading, post-dispatch fallback, lowest-cost or free-only routing, and spend policies before contacting a provider.

The gateway preserves native `server_tool_use` and paired `web_search_tool_result` blocks, caller metadata, encrypted result content needed for later turns, citations, `pause_turn`, embedded web-search error blocks, and `usage.server_tool_use.web_search_requests`. For dynamic filtering it also validates and preserves `code_execution_requests`; Anthropic provisions that execution and does not charge a separate code-execution fee for this web-tool use. Reported successful search calls are stored separately from model tokens. Search-call cost remains unknown because the price model has no versioned provider-search rate. An Anthropic search failure remains an HTTP 200 Message with a `web_search_tool_result_error`; unsuccessful searches are not billed or counted as completed calls.

With `stream: true`, the gateway preserves the native Anthropic sequence: `message_start`, ordered content-block frames, terminal `message_delta`, then `message_stop`. A direct `server_tool_use` starts as a content block and streams its query through `input_json_delta`. Its paired `web_search_tool_result` arrives as a complete block in `content_block_start`. Text and `citations_delta` frames remain native. Terminal `message_delta.usage` is cumulative and includes `server_tool_use.web_search_requests`. The gateway rewrites `message_start.message.model` to the requested public model while streaming; ping, error, content, usage, and unknown future event payloads otherwise pass through unchanged.

## Native web fetch

JSON and SSE Messages may contain one `{ "type": "web_fetch_20250910", "name": "web_fetch" }` basic tool, `{ "type": "web_fetch_20260209", "name": "web_fetch" }` dynamic-filtering tool, `{ "type": "web_fetch_20260309", "name": "web_fetch" }` dynamic-filtering tool with cache bypass, or `{ "type": "web_fetch_20260318", "name": "web_fetch" }` dynamic-filtering tool with cache bypass and response inclusion. `max_uses` is required from 1 through 4, and `max_content_tokens` is required from 1 through 16,384. The latter is an approximate text-extraction limit and does not bound binary PDF input. The dynamic versions use the same caller rules as dynamic web search and default to Anthropic-managed code execution. The `use_cache` boolean is accepted only on the cache-bypass versions, and `response_inclusion` with a value of `full` or `excluded` only on `web_fetch_20260318`. A request may allow or block up to ten plain hostnames, never both, and may enable or disable citations. Web fetch cannot be combined with web search, prompt caching, or a gateway-owned Message Batch item.

The public and upstream model must publish `chat` and `web_fetch`; code-execution invocation additionally requires `web_fetch_dynamic` on both, `use_cache` additionally requires `web_fetch_cache_bypass`, and `response_inclusion` additionally requires `web_fetch_response_inclusion`. The target must use the Anthropic adapter and built-in `anthropic` preset. The key needs `chat:generate` and `messages:web_fetch`. Optional `strict` must be boolean; deferred tool loading remains unsupported. The gateway preserves native server-tool use, caller metadata, fetched text or PDF document blocks, citations, embedded fetch errors, terminal token/fetch/code-execution usage, and the requested public model alias. It does not translate or retry a dispatched fetch. Because PDF input has no hard request-time token bound, token/spend policies, lowest-cost routing, and free-only routing reject fetch requests before dispatch. Provider-reported usage is still charged through the ordinary versioned token price; `usage.server_tool_use.web_fetch_requests` is retained as tool activity.

## Native prompt caching

Messages sent to the fixed Anthropic preset may use top-level automatic `cache_control` or explicit controls on tools, system blocks, and message content blocks. Controls are bounded to `{ "type": "ephemeral", "ttl": "5m" | "1h" }`, with the documented four-breakpoint and TTL-order rules. The public model and upstream model must both publish `prompt_cache`; translated targets are ineligible, and a dispatched cached request never falls back to another target because duplicate cache writes can be billable.

Accounting treats `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` as total input. It also preserves cache creation, cache read, five-minute creation, and one-hour creation counters in authoritative attempts and the data projection. A read-only cache hit can be costed when its snapshotted price includes a cache-read rate. Positive cache creation remains unknown until the price contract includes separate five-minute and one-hour creation rates, so cache-controlled requests still reject spend policies, free-only routes, and lowest-cost routing before dispatch. The gateway stores counters only, never prompt or cached content.

Sources: [Anthropic API](https://platform.claude.com/docs/en/api/overview), [Message Batches](https://platform.claude.com/docs/en/api/messages/batches), [batch processing](https://platform.claude.com/docs/en/build-with-claude/batch-processing), [streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool), [web fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool), and [server tools](https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools).
