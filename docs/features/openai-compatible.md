# OpenAI-compatible feature

Root: /api/openai/v1 · Backend owner: internal/gateway · Requirement: API-01/02/03

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

Chat, embeddings, models, and Responses generation can target OpenAI-compatible, Anthropic, or Gemini providers while keeping OpenAI response and event shapes. Moderations preserve the upstream OpenAI taxonomy and run only on a moderation-capable native OpenAI target. `POST /images/generations` supports native non-streaming JSON requests through image-capable OpenAI targets, requires an explicit public model and `images:generate`, and keeps request and response bodies within 16 MiB. `POST /images/edits` accepts 1-16 validated PNG/JPEG/WebP inputs plus an optional same-size PNG mask within a 64 MiB aggregate body, requires an explicit public model, prompt, `images:edit`, and a capable native GPT Image target, preserves provider multipart fields, and buffers at most 16 MiB. `POST /images/variations` accepts one square PNG under 4 MB, requires an explicit public model and `images:variation`, and uses upstream `dall-e-2` for the OpenAI preset; custom compatible endpoints remain operator-declared. `POST /audio/speech` accepts built-in OpenAI voices, requires `audio:speech` and an audio-speech-capable OpenAI target, and returns the provider's audio content type after buffering at most 16 MiB; SSE and provider-owned custom voice references are rejected. `POST /audio/transcriptions` accepts one supported audio file up to 25 MB, requires `audio:transcribe` and an audio-transcription-capable native OpenAI target, preserves provider multipart fields, streams provider events directly, and limits non-streaming responses to 16 MiB. `POST /audio/translations` uses the same upload limits, requires `audio:translate` and an audio-translation-capable native OpenAI target, preserves translation fields, buffers at most 16 MiB, and rejects streaming. The OpenAI preset accepts that capability only for upstream model `whisper-1`; custom OpenAI-compatible endpoints remain operator-declared. These media requests have no portable token or price contract, so active token, output-token, spend, free-only, and lowest-cost policies reject them; requests never fall back after dispatch. DALL-E 2 edits, image streaming, and video remain separate pending contracts. Non-streaming Responses are stored locally for the creating API key by default and support retrieval, input-item listing, cancellation, and deletion; `store:false` disables that stored Response resource and supports streaming. A request without a Conversation retains no response content. `background:true` uses a bounded durable SQLite queue and rechecks key grants, limits, and provider configuration before dispatch. The gateway disables upstream storage.

One native `web_search` tool may accompany function tools on a route to an OpenAI adapter using the built-in `openai` preset when both model layers publish `chat` and `web_search` and the key has `responses:web_search`. The request must set integer `max_tool_calls` from 1 through 4 and positive integer `max_output_tokens`. Direct input and instructions, metadata, function tools, tool choice/parallel-call settings, text and reasoning settings, sampling, truncation, storage, and background mode remain native OpenAI fields. Supported web-search fields are `search_context_size` (`low`, `medium`, or `high`), boolean `external_web_access`, `filters.allowed_domains` or `filters.blocked_domains` (up to 100 domain names), an approximate `user_location` with optional city, country, region, and timezone, and `return_token_budget: "default"`. `include` may contain only `web_search_call.action.sources`. Native `web_search_call` output, search/open/find actions, citations, sources, and token usage pass through; completed search calls are counted separately and their cost remains unknown. Stateless JSON, default gateway storage for 30 days, and durable background execution are supported with upstream `store:false`. Streaming, Conversation attachment, previous-response/provider references, preview tool names, image search, unlimited search-token return, translation, post-dispatch fallback, free-only or lowest-cost routing, spend policies, and unbounded output or tool calls are rejected before dispatch. Other hosted tools remain unavailable.

`POST /responses/compact` supports inline model/input compaction on the native OpenAI preset. It is not translated or sent to custom-compatible targets, and response, conversation, item, file, and container references are rejected rather than resolved with a shared provider credential.

`POST /responses/input_tokens` counts direct model/input requests on a native capable OpenAI target. It requires `tokens:count`; provider-owned response, conversation, item, file, and container references are rejected. Function-tool schemas are supported, while hosted tools remain unavailable.

Gateway-owned Conversations support create/retrieve/update/delete plus bounded item creation, retrieval, deletion, and cursor pagination. Message, function-call, and string function-output items are accepted; unsupported provider resource types and projected provider fields are rejected. Every resource belongs to the creating API key, retained storage has global/owner/key caps, and deleted content is purged after 30 days. Synchronous, durable background, and `store:false` streaming Responses prepend local history. Successful requests atomically append the new input and completed output. Attached streams are bounded and replay a successful terminal event only after the turn is stored; `response.failed` and `error` events are replayed without changing the Conversation.

## Gateway-owned batch files

The five stable Files operations—create, list, retrieve, content, and delete—store local resources owned by the creating inference key. Every operation requires `files:manage`; expired, deleted, missing, and foreign-key IDs all return the same not-found boundary. Upload accepts exactly one non-empty `.jsonl` file with `purpose=batch`, limits the file to 16 MiB and the complete multipart body to 17 MiB, and returns `status: "processed"`, so the pinned SDK's `waitForProcessing()` completes without provider polling.

Files expire 30 days after creation by default. `expires_after` may shorten retention with `anchor: "created_at"` and `seconds` from 3,600 through 2,592,000; the official SDK serializes these as the multipart fields `expires_after[anchor]` and `expires_after[seconds]`. List accepts `after`, `limit` from 1 through 100 (default 20), `order` as `asc` or `desc` (default `desc`), and `purpose` as `batch` or `batch_output`; the SDK's cursor iterator follows `has_more` by sending the last item ID as `after`. Content returns the exact stored bytes as an uncached attachment with the validated filename, and deletion is permanent. Gateway-owned Batches create `batch_output` Files for successful and failed request lines. General uploads, other upload purposes, provider-owned Files, and Vector Stores remain separate contracts.

## Gateway-owned Responses Batches

The four official Batch methods—create, retrieve, list, and cancel—manage local resources owned by the creating key. Creation requires `batches:manage` plus `responses:generate` and references one unexpired `purpose=batch` input File owned by that key. The first bounded contract accepts only `completion_window: "24h"`, `endpoint: "/v1/responses"`, optional metadata, and optional `output_expires_after` with `anchor: "created_at"` and 3,600–2,592,000 seconds.

An input File contains one to four JSONL envelopes with unique 1–64-byte UTF-8 `custom_id` values, `method: "POST"`, URL `/v1/responses`, one public model across the File, and non-streaming inline input. Each item rechecks the key's current grants and runs once through ordinary local Responses routing, admission, accounting, and prices with upstream storage disabled. Invalid JSONL envelopes or mixed models fail Batch validation; request-specific Responses validation or provider errors are reported asynchronously in the error output File. Nested background work, Conversations, provider-owned references, hosted tools, and local File references are rejected. Success and failure lines are written out of order to separate same-key `purpose=batch_output` Files and retain their `custom_id`. Cancellation permits already-dispatched work to finish and keeps partial output; unfinished items expire after 24 hours. Batch metadata remains available for 30 days. Output Files remain for 30 days by default or the requested shorter interval.

This is local compatibility rather than OpenAI's provider Batch service. The gateway does not claim the provider's 50% discount, separate rate pool, 50,000-request or 200 MB ceilings, or unlimited output-token behavior. Additional Batch endpoints, larger inputs, and provider-owned Batch execution remain pending.

An OpenAI-shaped client may target native OpenAI, Anthropic, Gemini, or a certified compatible endpoint. Return OpenAI-shaped output in every case. Preserve model aliases, tool correlation, usage provenance, and error class; reject unmappable semantics before dispatch.

Automated coverage includes the pinned OpenAI SDK URL for Chat, Responses, bounded native Responses web search in stateless/stored/background modes, Responses input-token counting, Moderations, image generation, speech, JSON/streaming audio transcription, audio translation, gateway-owned Files, and gateway-owned Responses Batches. Files coverage includes upload, processing wait, filters, order, automatic pagination, exact content, deletion, and key isolation. Batch coverage includes create/retrieve, list pagination, cancellation, result/error File downloads, scope enforcement, and key isolation. The suite also covers stored and background lifecycle calls, all provider-family translation directions, fragmented tool arguments, stream lifecycle events, native errors, embeddings where supported, and filtered model lists.

Sources: [Chat API](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions), [Responses guide](https://developers.openai.com/api/docs/guides/migrate-to-responses), [Web search guide](https://developers.openai.com/api/docs/guides/tools-web-search), [Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create), [Files create reference](https://developers.openai.com/api/reference/resources/files/methods/create), [Batches API](https://developers.openai.com/api/reference/resources/batches), [Batch guide](https://developers.openai.com/api/docs/guides/batch), [Speech API](https://developers.openai.com/api/reference/resources/audio/subresources/speech/methods/create), [Transcriptions API](https://developers.openai.com/api/reference/resources/audio/subresources/transcriptions/methods/create), [Translations API](https://developers.openai.com/api/reference/resources/audio/subresources/translations/methods/create).
