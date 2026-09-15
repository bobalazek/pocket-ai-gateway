# Anthropic-compatible feature

Root: /api/anthropic/v1 · SDK base root: /api/anthropic · Backend owner: internal/features/gateway

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

## Native prompt caching

Messages sent to the fixed Anthropic preset may use top-level automatic `cache_control` or explicit controls on tools, system blocks, and message content blocks. Controls are bounded to `{ "type": "ephemeral", "ttl": "5m" | "1h" }`, with the documented four-breakpoint and TTL-order rules. The public model and upstream model must both publish `prompt_cache`; translated targets are ineligible, and a dispatched cached request never falls back to another target because duplicate cache writes can be billable.

Accounting treats `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` as total input. It also preserves cache creation, cache read, five-minute creation, and one-hour creation counters in authoritative attempts and the data projection. Cache-controlled attempts have unknown cost until the price model can represent versioned cache rates, so spend policies, free-only routes, and lowest-cost routing reject them before dispatch. The gateway stores counters only, never prompt or cached content.

Sources: [Anthropic API](https://platform.claude.com/docs/en/api/overview), [streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).
