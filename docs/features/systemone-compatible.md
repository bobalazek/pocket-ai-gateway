# System One decisions

Owns `/api/systemone/v1`: the typed-decision wire format used by TypeSafe Jev, Laya, Kev, CLM, and OpenJev ([ADR-064](../project/decisions/2026-09-25-system-one-decision-models.md)). System One models return calibrated probabilities for yes/no (`noul`), multiple-choice, and ordered-score questions; they do not generate text. Authentication, limits, routing, and accounting are shared with the other inference roots.

## Request

~~~json
{"model":"triage","state":"Help! My payouts have been failing for 3 days.",
 "questions":{"is_urgent":{"type":"noul","instructions":"Does this convey urgency?",
   "criteria":{"true":"Explicitly time-sensitive","false":"No urgency expressed"}},
  "team":{"type":"choice","instructions":"Which team should handle this?",
   "criteria":{"billing":"Payments and payouts","technical":"Bugs and outages"}}}}
~~~

`model` is a public model ID. `state` and every `instructions` value may be a string, object, or array. A request holds 1–64 questions with 1–128-byte IDs. `choice` needs 1–255 options, `score` needs 2–10 ordered levels, and `noul` criteria are optional `true`/`false` descriptions. The gateway rejects malformed questions, `stream`, and unknown question types with 422 before admission; the provider validates everything else. Each question counts as one item for `batch_items` policies.

## Response

The provider's JSON passes through unchanged, including its versioned `model`:

~~~json
{"model":"jev-1.13.0","answers":{"is_urgent":{"type":"noul","noul":0.95},
 "team":{"type":"choice","choice":"billing","probabilities":{"billing":0.88,"technical":0.12},"confidence":0.81}},
 "usage":{"input_tokens":296,"output_tokens":20}}
~~~

Provider usage is recorded when present. Laya omits usage, so the gateway records an input estimate, as it does for moderations. Jev bills input only, so configure its output price as zero. Errors use `{"detail":{"error_type":"...","message":"..."}}`. Upstream errors are normalized to that shape from each server's format: Jev's `detail` object, FastAPI string or list `detail` (Laya), Vercel's top-level `error_type`/`message`, and OpenJev's `error` object. Upstream 400/409/422/429 keep their status.

## Setup

Choose a preset that fills the reviewed base URL:

| Preset | Base URL | Models | Notes |
| --- | --- | --- | --- |
| TypeSafe (Jev) | `https://api.typesafe.ai/v1` | `jev-latest`, `jev-1.13.0` | Hosted; API key required; input billed, output free |
| Vercel AI Gateway (Jev) | `https://ai-gateway.vercel.sh/typesafe/v1` | `typesafe-ai/jev` | Hosted Jev through a Vercel AI Gateway key |
| Codiv (OpenJev, Laya, Verdict) | `https://api.codiv.ai/v1` | `openjev-latest`, `laya-1.0`, `verdict-1.4` | Hosted OpenJev server; API key required |
| Laya (self-hosted) | `http://127.0.0.1:8000/v1` | `english`, `multilingual`, `typed-decisions` | `laya-serve`; key only if `LAYA_API_KEY` is set; no model list or usage |
| Kev (self-hosted) | `http://127.0.0.1:8009/v1` | the served checkpoint, such as `jaredpalmer/kev-4b` | `python -m kev.serve`; key only if `KEV_API_KEY` is set |
| CLM (self-hosted) | `http://127.0.0.1:8700/v1` | `clm-latest`, `clm-raw` | `clm-serve`; key only if `CLM_API_KEY` is set |
| OpenJev (self-hosted) | your server, for example `http://127.0.0.1:8081/v1` | `openjev-latest` and the server's other IDs | Both open projects named OpenJev; their default ports (8080 and 3000) may collide with the gateway, so set the URL explicitly |

Then:

1. Add the upstream model with **Typed decisions** capability and publish a public model with the same capability.
2. Create a key with `decisions:generate` (plus `models:read` to list models).
3. Set a price per upstream model. Jev bills input only, so its output price is zero; self-hosted servers usually cost nothing per token.

TypeSafe's SDK reads its base URL from `TYPESAFE_BASE_URL`; point it at `<instance>/api/systemone` and use the gateway key as the API key.

Licenses differ: Laya, Kev, CLM, and razorback16/openjev are Apache-2.0, while the openjev/openjev weights are CC BY-NC 4.0 (non-commercial). The gateway only forwards requests; check each model's license for your use.

## Boundaries

No streaming, no translation to or from chat formats, and no fallback to targets without the decisions capability. Cloudflare Workers AI's `typesafe/jev` uses Cloudflare's own API and is not a supported target. Tests cover forwarding, validation, both error formats, capability gating, and accounting against local mocks; live Jev and Laya calls are not certified.
