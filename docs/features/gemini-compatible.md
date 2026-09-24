# Gemini-compatible feature

Root: /api/gemini/v1beta · SDK root: /api/gemini with API version v1beta · Backend owner: internal/gateway

Own native Gemini URL actions, contents/parts/candidates/usageMetadata/errors, tool/stream codecs, model discovery/pagination, native upstream encoding, and fixtures. Gemini's OpenAI-compatible endpoint is a separate optional upstream preset.

## Illustrative generation exchange

POST /api/gemini/v1beta/models/assistant:generateContent, with x-goog-api-key gateway-key:

~~~json
{"contents":[{"role":"user","parts":[{"text":"Say hello."}]}],"generationConfig":{"maxOutputTokens":32}}
~~~

Response shape:

~~~json
{
  "candidates":[{"content":{"role":"model","parts":[{"text":"Hello."}]},"finishReason":"STOP","index":0}],
  "usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6},
  "modelVersion":"assistant"
}
~~~

Errors retain Google's native status structure:

~~~json
{"error":{"code":400,"message":"The requested feature is unavailable for this route.","status":"INVALID_ARGUMENT"}}
~~~

## Streaming, tools, and other operations

streamGenerateContent?alt=sse emits incremental native response envelopes; it is not an OpenAI delta stream. countTokens, embedContent, batchEmbedContents, and models/list/detail have distinct schemas under the same namespace.

## Gateway-owned Files

The Google Gen AI SDK can use `files.upload`, `files.list`, `files.get`, and `files.delete` against the SDK root `/api/gemini` with `apiVersion: "v1beta"`. Upload starts at `POST /api/gemini/upload/v1beta/files` and continues at the returned `X-Goog-Upload-URL`; `upload`, `finalize`, and `upload, finalize` commands use declared offsets. The 144-bit random upload URL is a one-hour capability: the official REST follow-up may omit the key header, while a supplied key must match the creating key. Keep that URL out of logs and shared links. Repeated identical chunks and finalization return the prior result. The returned URL uses the server's public origin; configure `POCKET_AI_GATEWAY_PUBLIC_URL` to the external HTTPS origin behind a proxy. A directly constructed handler without a configured origin falls back to the request's scheme and Host. Duplicate metadata keys are rejected. Parameterized MIME values such as `text/plain;charset=utf-8` are parsed and stored as the base media type. Optional SDK `config.name` is preserved when it is `files/{id}` with a 1–40 character lowercase alphanumeric/dash ID; the same name may be used by different keys, but a duplicate on one key conflicts. Generated names follow the same ID shape. A `files:manage` inference key owns each encrypted upload session and File, and is required again when resolving a File URI. Individual files are 1–8 MiB, upload sessions become inaccessible after one hour, and completed Files become inaccessible after 48 hours; expired encrypted rows are pruned by scheduled/manual retention and opportunistically on later uploads. The gateway does not import remote Files or expose their bytes through a download endpoint.

The returned `uri` is an opaque `pag-gemini://files/{id}` value for this gateway, not a provider URL. Put it in `fileData.fileUri` (or `file_data.file_uri`) in a `contents[].parts[]` entry for `generateContent`, `streamGenerateContent`, or `countTokens`. The gateway resolves at most four same-key references totaling 8 MiB, replaces them with inline data, enforces the 16 MiB expanded request bound, and admits usage against that expanded request. JSON traversal stops above 64 nesting levels or 100,000 values. These file-backed calls require a capable native Gemini target. Foreign URIs, cross-key references, unsupported nesting, and file-backed Interactions fail before provider dispatch. Media acceptance and token usage are ultimately reported by the selected Gemini provider; there is no OCR or provider-side File lifecycle here.

`GET /api/gemini/v1beta/live` is a native HTTP/1.1 WebSocket relay for Gemini `BidiGenerateContent`. The client authenticates with its gateway `x-goog-api-key` and sends official `setup` JSON first with `model: "models/{public_model}"`. The gateway resolves and rewrites that model, authenticates the Gemini upstream without exposing its credential, and then relays text, audio, video, tool, resumption, and server-content messages unchanged. The model and key need `realtime` and `realtime:connect`. Frames are limited to 16 MiB and sessions to ten minutes. Cumulative `usageMetadata` settles token usage when present; a setup-complete session without token metadata remains successful with unknown usage. Ephemeral-token, direct WebRTC, and cross-protocol translation flows are not exposed.

Gemini image output remains available through native `generateContent` requests and capable Gemini models, preserving inline-data response parts. Veo video generation uses the provider-neutral `POST /api/v1/media/jobs` lifecycle with a fixed Gemini model carrying `media_jobs`. A simple input object is wrapped as one `instances` item while top-level `parameters` are preserved; callers may also provide native `instances`. The worker submits `predictLongRunning`, polls the returned operation name, encrypts the terminal response for the creating key, and records unknown cost unless a future provider contract reports it. Cancellation stops local polling because the reviewed Gemini long-running operation contract has no corresponding video cancellation method.

`POST /api/gemini/v1beta/interactions` supports the Google Gen AI SDK's synchronous stateless text contract through a native model on the built-in Gemini preset. The public and upstream models need the `interactions` capability and the key needs `chat:generate`. The gateway accepts a non-empty string input, optional `generation_config.max_output_tokens`, false or omitted `store`, `stream`, and `background`, and an empty tools list. It forces upstream storage off, exposes the public model alias, accepts only completed or incomplete terminal responses, requires provider usage, and never falls back after a malformed terminal response. Provider state, agents, tools, media, structured output, streaming, and background execution remain separate contracts.

Interaction accounting records `total_input_tokens` and `total_cached_tokens` directly. Billable output is `total_tokens - total_input_tokens`, so reported thinking tokens cannot disappear from cost accounting. The provider totals must equal input plus output plus thinking; unexplained or positive tool-use tokens are rejected because this text-only contract does not dispatch tools.

Map systemInstruction, contents/parts, functionCall/functionResponse, candidate finish/safety metadata, and usage to/from OpenAI and Anthropic. Preserve valid thought signatures/provider-affine content; reject incompatible replay rather than discarding it. Distinguish generated token counts from thinking/cache details according to the actual target.

The gateway keeps `usageMetadata` unchanged and separately normalizes `cachedContentTokenCount` into request accounting. `promptTokenCount` remains the total effective prompt size, including cached content, so the cache count is recorded as a subset and never added twice.

When native clients use a query-string key, redact it before all logs/traces/captures. Prefer header authentication in docs. The path's public model resolves through ordinary grants and cannot be an arbitrary upstream resource.

Automated coverage pins the Google Gen AI SDK and verifies its exact `/api/gemini/v1beta` request path, then exercises every provider family, native stream/error/tool shapes, authorized model lists, signature rejection, credential replacement, bidirectional Live audio, cumulative usage, and Veo long-running operation polling.

Sources: [Interactions API](https://ai.google.dev/api/interactions-api), [Interactions overview](https://ai.google.dev/gemini-api/docs/interactions-overview), [generation API](https://ai.google.dev/api/generate-content), [Live API](https://ai.google.dev/api/live), [Veo](https://ai.google.dev/gemini-api/docs/veo), [function calling](https://ai.google.dev/gemini-api/docs/function-calling), [thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures).
