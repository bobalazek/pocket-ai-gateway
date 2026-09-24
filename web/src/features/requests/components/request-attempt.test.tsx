import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RequestAttemptDetails } from "@/features/requests/components/request-attempt";
import type { RequestAttempt } from "@/features/requests/types/requests.types";

const attempt: RequestAttempt = {
  id: "att_1",
  ordinal: 1,
  connection_id: "conn_1",
  model_id: "assistant",
  upstream_model_id: "provider-model",
  target_dialect: "openai",
  target_operation: "chat/completions",
  translation_applied: false,
  request_tool_count: 0,
  response_tool_call_count: 0,
  tool_call_status: "none",
  selection_reason: "fixed",
  rejected_candidates: [],
  state: "succeeded",
  usage_status: "provider_reported",
  input_tokens: 3,
  output_tokens: 5,
  cache_creation_input_tokens: null,
  cache_read_input_tokens: null,
  cache_creation_5m_input_tokens: null,
  cache_creation_1h_input_tokens: null,
  web_search_max_calls: null,
  web_search_call_count: null,
  estimated_cost_usd: "0.00004",
  as_recorded_cost_usd: "0.000026",
  restated_cost_usd: "0.00003",
  cost_usd: "0.000031",
  recorded_price: null,
  restated_price: null,
  started_at: "2026-09-15T12:00:00Z",
};

describe("RequestAttemptDetails", () => {
  it("keeps current, recorded, and restated costs distinct", () => {
    const html = renderToStaticMarkup(<RequestAttemptDetails attempt={attempt} clientDialect="openai" />);

    expect(html).toContain("$0.000031");
    expect(html).toContain("As recorded: $0.000026");
    expect(html).toContain("Restated: $0.00003");
  });

  it("distinguishes unknown usage from known zero", () => {
    const unknown = renderToStaticMarkup(<RequestAttemptDetails attempt={{ ...attempt, input_tokens: null, output_tokens: null, cost_usd: null }} clientDialect="openai" />);
    const zero = renderToStaticMarkup(<RequestAttemptDetails attempt={{ ...attempt, input_tokens: 0, output_tokens: 0 }} clientDialect="openai" />);

    expect(unknown).toContain("Token usage unavailable");
    expect(unknown).toContain("Cost unavailable");
    expect(unknown).not.toContain("0 tokens");
    expect(zero).toContain("0 tokens");
  });

  it("shows cache and web-search price terms with null distinct from zero", () => {
    const price = {
      id: "price_1",
      source: "operator",
      input_usd_per_million: "1",
      cache_read_usd_per_million: "0.2",
      output_usd_per_million: "2",
      web_search_usd_per_call: null,
      effective_from: "2026-09-15T00:00:00Z",
      effective_to: null,
    };
    const html = renderToStaticMarkup(<RequestAttemptDetails attempt={{
      ...attempt,
      recorded_price: price,
      restated_price: { ...price, id: "price_2", cache_read_usd_per_million: null, web_search_usd_per_call: "0" },
    }} clientDialect="openai" />);

    expect(html).toContain("Cache read $0.2/M");
    expect(html).toContain("Web search unpriced");
    expect(html).toContain("Cache read unpriced");
    expect(html).toContain("Web search $0/call");
  });
});
