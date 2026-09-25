# Set up a gateway

This guide takes a new instance from first start to a working API key. Pocket AI Gateway is alpha software: test it with low provider-side spending caps before relying on its limits. Every step is available in the dashboard at `/_/` and through the management API under `/api/v1` ([OpenAPI](../reference/openapi.yaml)). To install and run the server, see [deployment](deployment.md).

## 1. Claim the owner account

Open `/_/` and create the owner account. Until this is done, **anyone who can reach the server can claim it**, so keep the port on loopback or a private network until setup is complete. The first claim wins; later claims are rejected.

## 2. Invite people (optional)

| Role | Can do |
| --- | --- |
| Owner | Everything, including settings, backups, configuration import/export, instance-wide limits, owner transfer, and `env:`/`file:` credential references. There is exactly one owner. |
| Admin | Manage members, providers, models, routes, prices, and user, key, or connection limits; read audit and all usage. Cannot manage the owner or other admins, or change owner-only settings. |
| Member | Create and use their own API keys within the grants an owner or admin gave them; see only their own requests, usage, and analytics. |

Add users under **Users**. Each new user gets a one-time activation code to choose a password; the gateway sends no email. A member's **grants** (scopes, model patterns, and provider connections, or unrestricted) are the ceiling for every key they create. Narrowing a grant also narrows their existing keys.

## 3. Connect a provider

Under **Providers**, add a connection. A preset fills the reviewed base URL and limits the connection to the operations that provider supports:

- **OpenAI, Anthropic, Gemini:** native APIs.
- **OpenRouter, Z.AI, MiniMax, Mistral, Groq, DeepSeek, xAI, Together, Fireworks, Cohere, Perplexity:** OpenAI-compatible APIs.
- **Azure OpenAI, Amazon Bedrock, Google Vertex AI:** your resource URL plus an API key or rotating bearer token.
- **Ollama and Laya:** local servers on a private network.
- **Replicate:** asynchronous media jobs.
- **TypeSafe (Jev):** typed decisions.

Then store the credential. It is encrypted at rest and never shown again. The owner may instead reference `env:NAME` or `file:/absolute/path`, which the gateway rereads on every request; `bearer-env:` and `bearer-file:` suit short-lived cloud tokens.

Connection settings:

- **Timeout** (`timeout_ms`, 1–600 seconds, default 60): bounds a whole JSON response, or a stream's first response and each pause between chunks.
- **Allow private network:** needed only for local servers such as Ollama or Laya.

## 4. Add models

1. **Upstream models** belong to a connection: the provider's model ID (for example `gpt-5` or `jev-latest`) plus the capabilities it supports: Chat, Embeddings, Typed decisions, and so on. One connection can hold many models.
2. **Public models** are the names your clients use, such as `assistant` or `triage`. Publish one from an upstream model with the capabilities clients may use.
3. **Routes** decide which upstream model serves a public model:
   - **Fixed target:** always one model. Embeddings always use this, so vectors stay comparable.
   - **Ordered fallback:** try in order.
   - **Weighted:** split traffic by weight.
   - **Lowest estimated cost:** pick the cheapest known price.
   - **Lowest observed latency:** pick the fastest recently.

   Use **Route preview** to see which target would be chosen, and why others are rejected, without sending a request.

A client format can reach a provider in another format when the request translates cleanly. For example, the Anthropic SDK can use a Gemini model for chat. Features that cannot be translated are rejected with a 400 rather than approximated.

## 5. Set prices (optional)

Providers report tokens, not money. On the **Usage** page, add price versions per connection and upstream model: input, output, optional cache-read and web-search call rates, and an effective date. Weekly UTC windows are supported for off-peak pricing. Without a price, spend is **unknown**, never zero. Spend limits, lowest-cost routing, and free-only routes need prices. Historical requests can be repriced later.

## 6. Create API keys

Under **API keys**, create a key with:

- **Scopes:** which operations it may call (table below).
- **Model patterns:** public model IDs or globs such as `assistant` or `team-*`.
- **Connections:** which provider connections may serve it.
- **Expiry:** optional.

The secret (`pag_...`) is shown once. Rotate a key to replace its secret without changing its permissions, or revoke it. Keys only call inference roots; dashboard sessions only call `/api/v1`. The one exception is media jobs, which accept a key with `media:generate`.

| Scope | Allows |
| --- | --- |
| `chat:generate` | OpenAI Chat Completions, Anthropic Messages, Gemini `generateContent`/`streamGenerateContent`, Gemini Interactions |
| `responses:generate` | OpenAI Responses, background Responses, compaction, Conversations |
| `responses:web_search` / `responses:file_search` | Hosted web search, or file search over the key's Vector Stores, in Responses (plus `responses:generate`) |
| `completions:generate` | Legacy OpenAI Completions |
| `messages:batches` | Anthropic Message Batches (plus `chat:generate`) |
| `messages:web_search` / `messages:web_fetch` | Anthropic hosted web search or fetch (plus `chat:generate`) |
| `embeddings:generate` | OpenAI Embeddings, Gemini `embedContent`/`batchEmbedContents` |
| `moderations:classify` | OpenAI Moderations |
| `decisions:generate` | System One typed decisions (TypeSafe Jev, Laya) |
| `images:generate` / `images:edit` / `images:variation` | OpenAI image generation, edits, variations |
| `audio:speech` / `audio:transcribe` / `audio:translate` | Text to speech, transcription, translation |
| `realtime:connect` | OpenAI Realtime and Live, Gemini Live WebSockets |
| `media:generate` | Asynchronous media jobs (`/api/v1/media/jobs`) |
| `batches:manage` | OpenAI Batches (plus the scope of each batched endpoint) |
| `files:manage` | OpenAI, Anthropic, and Gemini Files and OpenAI Uploads |
| `vector_stores:manage` | OpenAI Vector Stores |
| `tokens:count` | OpenAI input-token, Anthropic, and Gemini token counting |
| `models:read` | Model lists on every root |

A key sees a model only when it has the operation's scope, matches the model pattern, and is granted a connection that serves the model. Otherwise the model is reported as unavailable (404).

## 7. Add limits (optional)

On the **Usage** page, attach limit policies to the whole instance (owner only), a user, a key, or a provider connection. Every matching policy must admit a request.

| Metric | Algorithms |
| --- | --- |
| Requests, tokens | Token bucket (rate with refill), fixed window, or quota per hour/day/week/month/lifetime |
| Spend (USD) | Quota per period; needs prices |
| Concurrency | Maximum requests in flight |
| Body bytes, output tokens, batch items | Per-request ceiling |

Tokens and spend are reserved from an estimate at admission and settled from provider usage afterwards. A request over a limit gets 429 in its protocol's error format.

## 8. Connect clients

Give applications the instance URL, a key, and a public model ID. Base URLs, SDK snippets, and error handling are in the [client skill](../../skills/pocket-ai-gateway/SKILL.md) and the [API reference](../reference/api.md). Every response carries `X-Pocket-AI-Request-ID`; find it under **Requests** to see the route, attempts, tokens, and cost.

## 9. Before you rely on it

- Set `POCKET_AI_GATEWAY_BACKUP_KEY`, enable scheduled backups in **Settings**, and keep the key and archives off the server. See [operations](operations.md).
- Put the gateway behind HTTPS with its public URL configured before exposing it beyond loopback.
- Watch **Status** for failing providers, backups, or storage.
