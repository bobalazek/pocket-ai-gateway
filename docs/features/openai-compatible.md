# OpenAI-compatible feature

Root: /api/openai/v1 · Backend owner: internal/features/openaicompat · Requirement: API-01/02/03

Own routes, OpenAI wire types/errors, Chat and Responses event codecs, model-list schema, upstream encoding, and fixtures. No login/session management, independent retry loop, or independent quota logic here.

## Illustrative Chat exchange

POST /api/openai/v1/chat/completions, with Authorization: Bearer gateway-key:

~~~json
{"model":"assistant","messages":[{"role":"user","content":"Say hello."}],"stream":false,"max_tokens":32}
~~~

Response shape:

~~~json
{
  "id":"chatcmpl_example",
  "object":"chat.completion",
  "created":0,
  "model":"assistant",
  "choices":[{"index":0,"message":{"role":"assistant","content":"Hello."},"finish_reason":"stop"}],
  "usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
}
~~~

Numbers/IDs are example values only. Actual output-limit fields and model-specific parameters are verified against the supported SDK/model matrix.

Errors preserve the OpenAI error envelope:

~~~json
{"error":{"message":"This key cannot access the requested model.","type":"invalid_request_error","param":"model","code":"model_not_found"}}
~~~

## Chat, Responses, and translation

Chat stream events contain chat.completion.chunk objects with choices/delta; function arguments may arrive as fragments. Responses has a different output-item and event lifecycle: response.created, output-item/content deltas, and terminal response status. Do not reuse Chat chunks as Responses events.

Expose /responses, /embeddings, and /models under the same OpenAI namespace with their own schemas. Native and translated stateless Responses input/tools/output/events are phase 5 requirements. Stateful resource operations are tracked in phase 8, with key ownership and provider affinity.

An OpenAI-shaped client may target native OpenAI, Anthropic, Gemini, or a certified compatible endpoint. Return OpenAI-shaped output in every case. Preserve model aliases, tool correlation, usage provenance, and error class; reject unmappable semantics before dispatch.

Acceptance: pinned OpenAI SDK tests for both Chat and Responses across all three families, native errors/SSE/tool cycles, embeddings where supported, filtered OpenAI model list, and exact prefixed request URLs.

Sources: [Chat API](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions), [Responses guide](https://developers.openai.com/api/docs/guides/migrate-to-responses).
