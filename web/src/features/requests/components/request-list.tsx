import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";

export function RequestList({ items, filters, next, onNavigate }: { items: GatewayRequest[]; filters: RequestFilters; next: string; onNavigate: (filters: RequestFilters) => void }) {
  return (
    <section className="section-block">
      <h2>{items.length} requests</h2>
      <div className="resource-list">
        {items.map((item) => <RequestCard key={item.id} item={item} />)}
      </div>
      <div className="row-actions section-block">
        {filters.cursor && <Button variant="outline" onClick={() => window.history.back()}>Previous page</Button>}
        {next && <Button variant="outline" onClick={() => onNavigate({ ...filters, cursor: next })}>Next page</Button>}
      </div>
    </section>
  );
}

function RequestCard({ item }: { item: GatewayRequest }) {
  return (
    <Card className="panel">
      <div className="resource-row-main">
        <div>
          <strong>{item.model_id}</strong>
          <small>{item.dialect} · {item.operation} · {item.state} · {new Date(item.started_at).toLocaleString()}</small>
          <code>{item.id}</code>
        </div>
        <span className="status-badge">{item.attempts.length} attempt{item.attempts.length === 1 ? "" : "s"}</span>
      </div>
      {item.attempts.map((attempt) => {
        const hasCacheUsage = attempt.cache_creation_input_tokens > 0 || attempt.cache_read_input_tokens > 0 || attempt.cache_creation_5m_input_tokens > 0 || attempt.cache_creation_1h_input_tokens > 0;
        return (
          <div className="resource-row section-block" key={attempt.id}>
            <div>
              <small>#{attempt.ordinal} · {attempt.connection_id} · {attempt.upstream_model_id} · {attempt.translation_applied ? `${item.dialect} → ${attempt.target_dialect}` : attempt.target_dialect} · {attempt.target_operation}</small>
              <small>{attempt.state} · {attempt.usage_status} · {attempt.input_tokens + attempt.output_tokens} tokens · {attempt.cost_usd === null ? "cost N/A" : `$${attempt.cost_usd}`} · {attempt.request_tool_count} tools offered · {attempt.response_tool_call_count === 0 ? "no tool calls" : `${attempt.response_tool_call_count} tool calls ${attempt.tool_call_status}`}</small>
              {attempt.web_search_max_calls !== null && (
                <small>Web search: {attempt.web_search_call_count === null ? "count unavailable" : `${attempt.web_search_call_count} call${attempt.web_search_call_count === 1 ? "" : "s"}`} · maximum {attempt.web_search_max_calls}</small>
              )}
              {hasCacheUsage && <small>Cache: {attempt.cache_creation_input_tokens} written ({attempt.cache_creation_5m_input_tokens} at 5m, {attempt.cache_creation_1h_input_tokens} at 1h) · {attempt.cache_read_input_tokens} read</small>}
              <small>Selected by {attempt.selection_reason}{attempt.rejected_candidates.length ? ` · ${attempt.rejected_candidates.length} candidates rejected` : ""}</small>
            </div>
          </div>
        );
      })}
    </Card>
  );
}
