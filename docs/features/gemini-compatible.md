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

Map systemInstruction, contents/parts, functionCall/functionResponse, candidate finish/safety metadata, and usage to/from OpenAI and Anthropic. Preserve valid thought signatures/provider-affine content; reject incompatible replay rather than discarding it. Distinguish generated token counts from thinking/cache details according to the actual target.

When native clients use a query-string key, redact it before all logs/traces/captures. Prefer header authentication in docs. The path's public model resolves through ordinary grants and cannot be an arbitrary upstream resource.

Automated coverage pins the Google Gen AI SDK and verifies its exact `/api/gemini/v1beta` request path, then exercises every provider family, native stream/error/tool shapes, authorized model lists, signature rejection, and credential replacement.

Sources: [generation API](https://ai.google.dev/api/generate-content), [function calling](https://ai.google.dev/gemini-api/docs/function-calling), [thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures).
