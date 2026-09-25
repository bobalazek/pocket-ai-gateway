# System One decisions

Owns `/api/systemone/v1`: the typed-decision wire format used by TypeSafe Jev and self-hosted Laya ([ADR-064](../project/decisions/2026-09-25-system-one-decision-models.md)). System One models return calibrated probabilities for yes/no (`noul`), multiple-choice, and ordered-score questions; they do not generate text. Authentication, limits, routing, and accounting are shared with the other inference roots.

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

Provider usage is recorded when present. Laya omits usage, so the gateway records an input estimate, as it does for moderations. Jev bills input only, so configure its output price as zero. Errors use `{"detail":{"error_type":"...","message":"..."}}`; Laya's plain-string `detail` errors are normalized to that shape, and upstream 400/409/422/429 keep their status.

## Setup

1. Add a connection from the **TypeSafe (Jev)** preset (`https://api.typesafe.ai/v1`, API key required) or the **Laya (self-hosted)** preset (`http://127.0.0.1:8000/v1` by default; set a credential only if `LAYA_API_KEY` is configured).
2. Add the upstream model (`jev-latest`, `jev-1.13.0`, or Laya's `english`, `multilingual`, or `typed-decisions`) with **Typed decisions** capability, then publish a public model with the same capability.
3. Create a key with `decisions:generate` (plus `models:read` to list models).

TypeSafe's SDK reads its base URL from `TYPESAFE_BASE_URL`; point it at `<instance>/api/systemone` and use the gateway key as the API key.

## Boundaries

No streaming, no translation to or from chat formats, and no fallback to targets without the decisions capability. Cloudflare Workers AI's `typesafe/jev` uses Cloudflare's own API and is not a supported target. Tests cover forwarding, validation, both error formats, capability gating, and accounting against local mocks; live Jev and Laya calls are not certified.
