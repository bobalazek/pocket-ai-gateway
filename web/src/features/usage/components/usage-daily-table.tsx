import type { UsagePoint, UsageSummary } from "@/features/usage/types/usage.types";

export function UsageDailyTable({ usage, points }: { usage: UsageSummary; points: UsagePoint[] }) {
  const hasCache = usage.cache_creation_input_tokens > 0 || usage.cache_read_input_tokens > 0 || usage.cache_creation_5m_input_tokens > 0 || usage.cache_creation_1h_input_tokens > 0;
  const hasSearch = usage.web_search_calls > 0;
  return <div className="table-wrap" tabIndex={0} role="region" aria-label="Scrollable daily usage table">
    <table>
      <thead><tr>
        <th>Date</th><th>Requests</th><th>Failed</th><th>Error rate</th><th>Input</th><th>Output</th>
        {hasCache && <><th>Cache writes</th><th>Cache reads</th><th>5m writes</th><th>1h writes</th></>}
        {hasSearch && <th>Web search</th>}
        <th>Known spend</th><th>Needs review</th>
      </tr></thead>
      <tbody>{points.map((point) => <tr key={point.date}>
        <td>{point.date}</td><td>{point.requests.toLocaleString()}</td><td>{point.failed_requests.toLocaleString()}</td><td>{point.successful_requests + point.failed_requests ? `${point.error_rate_percent.toFixed(1)}%` : "—"}</td><td>{point.input_tokens.toLocaleString()}</td><td>{point.output_tokens.toLocaleString()}</td>
        {hasCache && <><td>{point.cache_creation_input_tokens.toLocaleString()}</td><td>{point.cache_read_input_tokens.toLocaleString()}</td><td>{point.cache_creation_5m_input_tokens.toLocaleString()}</td><td>{point.cache_creation_1h_input_tokens.toLocaleString()}</td></>}
        {hasSearch && <td>{point.web_search_calls.toLocaleString()}</td>}
        <td>${point.known_cost_usd}</td><td>{point.unknown_attempts.toLocaleString()}</td>
      </tr>)}</tbody>
    </table>
    {!points.length && <p className="empty-copy">No daily usage in this period.</p>}
  </div>;
}
