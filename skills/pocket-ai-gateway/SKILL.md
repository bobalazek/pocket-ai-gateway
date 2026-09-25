---
name: pocket-ai-gateway
description: Call AI models through a Pocket AI Gateway instance with OpenAI, Anthropic, Gemini, or TypeSafe SDKs or plain HTTP, including chat, streaming, embeddings, typed System One decisions (Jev, Laya), and model discovery. Use when writing or debugging application code that sends inference requests to a gateway. For instance health, usage statistics, backups, or restores, use pocket-ai-gateway-ops instead.
---

# Using Pocket AI Gateway as a client

A gateway exposes several provider wire formats under separate URL roots. The path picks the request and response format; the operator's route picks the upstream provider. A request to the Anthropic root may be served by Gemini and still returns an Anthropic-shaped response.

## Before writing code

1. Get the instance URL from the user or configuration; locally it is usually `http://127.0.0.1:8080`. Read `<instance>/llms.txt` for the running version's capabilities before assuming an endpoint exists.
2. Get a gateway API key (`pag_...`) from the user or an environment variable such as `POCKET_AI_GATEWAY_KEY`. Never hard-code it, print it, or put it in a URL you log. Provider keys (OpenAI, Anthropic, and so on) do not work here.
3. List the models the key may call (the key needs `models:read`). Model IDs are the operator's public aliases, such as `assistant`, not upstream names such as `gpt-5`:

```sh
curl -s "$GATEWAY/api/openai/v1/models" -H "Authorization: Bearer $POCKET_AI_GATEWAY_KEY"
```

Use only IDs from that list. A model that works on one root may be absent from another when no route target supports that format's operation.

## Roots, SDK settings, and authentication

| Client | Base URL | API key header |
| --- | --- | --- |
| OpenAI SDK | `<instance>/api/openai/v1` | `Authorization: Bearer <key>` |
| Anthropic SDK | `<instance>/api/anthropic` | `x-api-key: <key>` plus `anthropic-version: 2023-06-01` |
| Google Gen AI SDK | `<instance>/api/gemini` with API version `v1beta` | `x-goog-api-key: <key>` |
| TypeSafe SDK or HTTP (System One) | `<instance>/api/systemone` (set `TYPESAFE_BASE_URL`) | `Authorization: Bearer <key>` |

```python
from openai import OpenAI
client = OpenAI(base_url=f"{gateway}/api/openai/v1", api_key=os.environ["POCKET_AI_GATEWAY_KEY"])
reply = client.chat.completions.create(model="assistant", messages=[{"role": "user", "content": "Hello"}])
```

```python
import anthropic
client = anthropic.Anthropic(base_url=f"{gateway}/api/anthropic", api_key=os.environ["POCKET_AI_GATEWAY_KEY"])
```

```python
from google import genai
client = genai.Client(api_key=os.environ["POCKET_AI_GATEWAY_KEY"], http_options={"base_url": f"{gateway}/api/gemini", "api_version": "v1beta"})
```

Keys carry scopes and model/connection grants. When the key lacks the operation's scope (`chat:generate`, `embeddings:generate`, `responses:generate`, and so on) or a grant for the model, the model is treated as unavailable (404). A 403 means an extra tool scope is missing, such as `messages:web_search`. Ask the operator to widen the key rather than guessing other model IDs.

## Common operations

- **Chat:** OpenAI `POST /chat/completions` or `/responses`, Anthropic `POST /v1/messages`, Gemini `POST /v1beta/models/{model}:generateContent`. Tools, image input, structured output, and streaming translate across providers where the semantics survive; otherwise the gateway rejects the request with a 400 explaining the unsupported feature.
- **Streaming:** use each SDK's normal streaming call. Usage is accounted even when you do not request it; on OpenAI Chat, the final usage chunk appears only if you set `stream_options.include_usage`.
- **Embeddings:** OpenAI `POST /embeddings` with a string, string array (up to 2,048 items), or token arrays; or Gemini `embedContent` / `batchEmbedContents`. The model must be published with Embeddings capability. Embedding routes use one fixed target so vectors stay comparable: never mix vectors from different model IDs.
- **Typed decisions (System One):** `POST /api/systemone/v1/systemone` with `state` and 1–64 `questions`, each `noul` (yes/no probability), `choice` (1–255 options), or `score` (2–10 ordered levels). Use these models to classify, route, gate, or grade, not to write text: put any free-text field through a chat model instead. Requires `decisions:generate` and a model published with Typed decisions capability; list them with `GET /api/systemone/v1/models`. Answers include `probabilities` and `confidence`; set thresholds per model, because Jev and Laya calibrate confidence differently.

```sh
curl -s "$GATEWAY/api/systemone/v1/systemone" -H "Authorization: Bearer $POCKET_AI_GATEWAY_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"triage","state":"Payouts failing for 3 days","questions":{"urgent":{"type":"noul","instructions":"Is this urgent?"},"team":{"type":"choice","instructions":"Route it","criteria":{"billing":"Payments","technical":"Bugs"}}}}'
```

- **Other operations** (moderations, images, audio, realtime, batches, files, vector stores, media jobs) are listed with their limits in the instance's `llms.txt` and in `docs/reference/api.md`.

## Errors and retries

Errors use the root's native shape, so SDK error classes work unchanged. System One errors are `{"detail":{"error_type","message"}}`.

- **400/409/422:** fix the request; retrying the same body will fail again. Upstream client errors keep their status.
- **401:** missing or wrong gateway key. **403:** missing tool scope. **404:** unknown model, or one this key may not use for this operation.
- **429:** a gateway limit (requests, tokens, concurrency, or spend) or an upstream rate limit. Back off; the SDKs' built-in retry is fine.
- **502/503/504:** provider or gateway failure after routing and fallback. A bounded retry is reasonable.

Every admitted response carries `X-Pocket-AI-Request-ID`. Include it when reporting a problem; an operator can look it up in the dashboard's request history. Request history and cost live under `/api/v1`, which requires a dashboard session rather than an API key.

## Rules

- Keep the root, SDK, and header set consistent; one protocol's headers never switch another root's format.
- Treat unknown usage or price as unknown, not zero.
- Do not send secrets or personal data the user has not approved sending to an AI provider; the gateway forwards content to whichever provider the operator configured.
