import type { RequestAttempt, RequestPriceProvenance } from "@/features/requests/types/requests.types";

function PriceProvenance({ label, price }: { label: string; price: RequestPriceProvenance }) {
  return (
    <>
      <small>{label}: {price.source} · {price.id}</small>
      <small>Input ${price.input_usd_per_million}/M · Cache read {price.cache_read_usd_per_million === null ? "unpriced" : `$${price.cache_read_usd_per_million}/M`} · Output ${price.output_usd_per_million}/M · Web search {price.web_search_usd_per_call === null ? "unpriced" : `$${price.web_search_usd_per_call}/call`}</small>
      <small>Effective {new Date(price.effective_from).toLocaleString()} → {price.effective_to ? new Date(price.effective_to).toLocaleString() : "current"}</small>
    </>
  );
}

export function RequestAttemptDetails({ attempt, clientDialect }: { attempt: RequestAttempt; clientDialect: string }) {
  const recordedCost = attempt.as_recorded_cost_usd;
  const currentCost = attempt.cost_usd;
  const tokenUsage = attempt.input_tokens === null || attempt.output_tokens === null ? "Token usage unavailable" : `${attempt.input_tokens + attempt.output_tokens} tokens`;
  const cache: string[] = [];
  if (attempt.cache_creation_input_tokens !== null) {
    const details = attempt.cache_creation_5m_input_tokens !== null || attempt.cache_creation_1h_input_tokens !== null
      ? ` (${attempt.cache_creation_5m_input_tokens ?? 0} at 5m, ${attempt.cache_creation_1h_input_tokens ?? 0} at 1h)`
      : "";
    cache.push(`${attempt.cache_creation_input_tokens} written${details}`);
  }
  if (attempt.cache_read_input_tokens !== null) cache.push(`${attempt.cache_read_input_tokens} read`);

  return (
    <div className="resource-row section-block">
      <div>
        <strong>Attempt {attempt.ordinal}</strong>
        <small>{attempt.connection_id} · {attempt.upstream_model_id} · {attempt.translation_applied ? `${clientDialect} → ${attempt.target_dialect}` : attempt.target_dialect} · {attempt.target_operation}</small>
        <small>{attempt.state} · {attempt.usage_status} · {tokenUsage} · {currentCost === null ? "Cost unavailable" : `$${currentCost}`} · {attempt.request_tool_count} tools offered · {attempt.response_tool_call_count === 0 ? "no tool calls" : `${attempt.response_tool_call_count} tool calls ${attempt.tool_call_status}`}</small>
        {attempt.estimated_cost_usd != null && <small>Admission estimate: ${attempt.estimated_cost_usd}</small>}
        {recordedCost != null && <small>As recorded: ${recordedCost}</small>}
        {attempt.restated_cost_usd != null && <small>Restated: ${attempt.restated_cost_usd}</small>}
        {attempt.recorded_price && <PriceProvenance label="Recorded price" price={attempt.recorded_price} />}
        {attempt.restated_price && <PriceProvenance label="Restatement price" price={attempt.restated_price} />}
        {attempt.web_search_max_calls !== null && (
          <small>Web search: {attempt.web_search_call_count === null ? "count unavailable" : `${attempt.web_search_call_count} call${attempt.web_search_call_count === 1 ? "" : "s"}`} · maximum {attempt.web_search_max_calls}</small>
        )}
        {cache.length > 0 && <small>Cache: {cache.join(" · ")}</small>}
        <small>Selected by {attempt.selection_reason}{attempt.rejected_candidates.length ? ` · ${attempt.rejected_candidates.length} candidates rejected` : ""}</small>
        <code>{attempt.id}</code>
      </div>
    </div>
  );
}
