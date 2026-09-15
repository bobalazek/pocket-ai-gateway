# OpenAI-compatible feature

Root: /api/openai/v1 · Backend owner: internal/features/gateway · Requirement: API-01/02/03

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

Chat, embeddings, models, and Responses generation can target OpenAI-compatible, Anthropic, or Gemini providers while keeping OpenAI response and event shapes. Non-streaming Responses are stored locally for the creating API key by default and support retrieval, input-item listing, cancellation, and deletion; `store:false` remains stateless and supports streaming. `background:true` uses a bounded durable SQLite queue and rechecks key grants, limits, and provider configuration before dispatch. The gateway disables upstream storage. Hosted tools remain rejected.

`POST /responses/compact` supports inline model/input compaction on the native OpenAI preset. It is not translated or sent to custom-compatible targets, and response, conversation, item, file, and container references are rejected rather than resolved with a shared provider credential.

Gateway-owned Conversations support create/retrieve/update/delete plus bounded item creation, retrieval, deletion, and cursor pagination. Message, function-call, and string function-output items are accepted; unsupported provider resource types and projected provider fields are rejected. Every resource belongs to the creating API key, retained storage has global/owner/key caps, and deleted content is purged after 30 days. Conversation attachment to a Response remains explicitly unavailable until its context and output can be committed atomically.

An OpenAI-shaped client may target native OpenAI, Anthropic, Gemini, or a certified compatible endpoint. Return OpenAI-shaped output in every case. Preserve model aliases, tool correlation, usage provenance, and error class; reject unmappable semantics before dispatch.

Automated coverage includes the pinned OpenAI SDK URL for Chat and Responses, stored and background lifecycle calls, all provider-family translation directions, fragmented tool arguments, stream lifecycle events, native errors, embeddings where supported, and filtered model lists.

Sources: [Chat API](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions), [Responses guide](https://developers.openai.com/api/docs/guides/migrate-to-responses).
