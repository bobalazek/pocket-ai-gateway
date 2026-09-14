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

Translate to/from OpenAI and Gemini while preserving multiple tool calls/results, stop reasons, usage, refusals, and cancellation. Opaque thinking/signatures need an explicit valid mapping/affinity rule; unsupported ones are not silently removed. Count tokens using a capable target-specific path, with honest estimate semantics.

Acceptance: pinned Anthropic SDK tests across all three provider families; header/version validation; prefixed base URL with no duplicate /v1; Anthropic model pagination; native error/event/tool shapes; no header-selected dialect switching.

Sources: [Anthropic API](https://platform.claude.com/docs/en/api/overview), [streaming](https://platform.claude.com/docs/en/build-with-claude/streaming).
