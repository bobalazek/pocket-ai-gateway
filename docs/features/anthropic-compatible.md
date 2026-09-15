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

The stream follows message_start, ordered content_block_start/delta/stop events, message_delta, and message_stop. A tool_use block contains id/name/input; tool_result content links back to the original tool ID. Partial argument JSON is forwarded through input_json_delta, not flattened into text.

Translate ordinary JSON requests to/from OpenAI and Gemini while preserving multiple tool calls/results, stop reasons, usage, refusals, and cancellation. Streaming currently requires an Anthropic-compatible target: Anthropic requires input usage in its first event, while OpenAI and Gemini report it only at completion. Opaque thinking/signatures need an explicit valid mapping/affinity rule; unsupported ones are not silently removed. Count tokens using a capable target-specific path, with honest estimate semantics.

Automated coverage pins the Anthropic SDK and verifies that its `/v1/messages` path is appended exactly once, then exercises OpenAI, Anthropic, and Gemini upstream families, native headers/errors, translated tool cycles, and stream shapes.

## Gateway-owned Message Batches

The six Message Batches operations are available at `POST/GET /api/anthropic/v1/messages/batches`, `GET/DELETE /api/anthropic/v1/messages/batches/{id}`, `POST /api/anthropic/v1/messages/batches/{id}/cancel`, and `GET /api/anthropic/v1/messages/batches/{id}/results`. A submission contains one to four requests and is limited to 16 MiB. Each item has a unique `custom_id` matching `^[a-zA-Z0-9_-]{1,64}$` and a `params` object for an ordinary non-streaming Message with `max_tokens` of at least one.

The creating inference key owns the batch and needs `chat:generate` plus `messages:batches`. Each item executes locally through the ordinary Messages authorization, current grants, model routing, admission, and accounting path. The submission validates the outer batch envelope; invalid nested `params` become asynchronous per-item `errored` results, so one item failing does not prevent the remaining items from completing. This is gateway-owned asynchronous execution; it does not forward an Anthropic provider batch, inherit Anthropic's 100,000-item limit, or receive Anthropic's native 50% batch price.

New batches start `in_progress`. Cancellation moves unfinished work through `canceling`; work already dispatched may finish before the batch reaches `ended`. Until then, `request_counts` keeps every item in `processing` and its terminal counters at zero; the terminal distribution appears together at `ended`. The public `expires_at` is the processing deadline 24 hours after creation, when unfinished items become expired. Results stream as newline-delimited JSON objects shaped as `{ "custom_id": ..., "result": ... }`. Result types are `succeeded`, `errored`, `canceled`, or `expired`; order is not guaranteed, so clients match results through `custom_id`. Only an ended batch can be deleted. At 29 days after creation, the gateway deletes the whole batch and its results; it does not retain archived batch metadata.

## Native basic web search

JSON and SSE Messages may contain one direct `{ "type": "web_search_20250305", "name": "web_search" }` tool alongside ordinary application tools. `max_uses` is required and must be an integer from 1 through 4. `allowed_callers` may be omitted, which uses the basic tool's direct default, or be exactly `["direct"]`. Requests may provide up to 100 `allowed_domains` or `blocked_domains`, never both, and an approximate `user_location` with at least one location field. Prompt caching and web search cannot be combined in the same request.

The public and upstream model must both publish `chat` and `web_search`; the selected target must use the Anthropic adapter and built-in `anthropic` preset. The key needs `chat:generate` and `messages:web_search`. Eligible ordered, weighted, and latency strategies may choose a target before dispatch. The request rejects translation, dynamic-filtering web-search versions, post-dispatch fallback, lowest-cost or free-only routing, and spend policies before contacting a provider.

The gateway preserves native `server_tool_use` and paired `web_search_tool_result` blocks, encrypted result content needed for later turns, citations, `pause_turn`, embedded web-search error blocks, and `usage.server_tool_use.web_search_requests`. Reported successful search calls are stored separately from model tokens. Search-call cost remains unknown because the price model has no versioned provider-search rate. An Anthropic search failure remains an HTTP 200 Message with a `web_search_tool_result_error`; unsuccessful searches are not billed or counted as completed calls.

With `stream: true`, the gateway preserves the native Anthropic sequence: `message_start`, ordered content-block frames, terminal `message_delta`, then `message_stop`. A direct `server_tool_use` starts as a content block and streams its query through `input_json_delta`. Its paired `web_search_tool_result` arrives as a complete block in `content_block_start`. Text and `citations_delta` frames remain native. Terminal `message_delta.usage` is cumulative and includes `server_tool_use.web_search_requests`. Ping, error, and unknown future Anthropic event types are forwarded without being rewritten into a successful terminal event. Native SSE is byte-preserved, so `message_start.message.model` remains the provider-reported upstream model; public-model response rewriting is a separate compatibility change.

## Native prompt caching

Messages sent to the fixed Anthropic preset may use top-level automatic `cache_control` or explicit controls on tools, system blocks, and message content blocks. Controls are bounded to `{ "type": "ephemeral", "ttl": "5m" | "1h" }`, with the documented four-breakpoint and TTL-order rules. The public model and upstream model must both publish `prompt_cache`; translated targets are ineligible, and a dispatched cached request never falls back to another target because duplicate cache writes can be billable.

Accounting treats `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` as total input. It also preserves cache creation, cache read, five-minute creation, and one-hour creation counters in authoritative attempts and the data projection. A read-only cache hit can be costed when its snapshotted price includes a cache-read rate. Positive cache creation remains unknown until the price contract includes separate five-minute and one-hour creation rates, so cache-controlled requests still reject spend policies, free-only routes, and lowest-cost routing before dispatch. The gateway stores counters only, never prompt or cached content.

Sources: [Anthropic API](https://platform.claude.com/docs/en/api/overview), [Message Batches](https://platform.claude.com/docs/en/api/messages/batches), [batch processing](https://platform.claude.com/docs/en/build-with-claude/batch-processing), [streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool), and [server tools](https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools).
