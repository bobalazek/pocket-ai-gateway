import Link from "next/link";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { RankedChart, rankFormat, type RankMetric } from "@/features/analytics/components/analytics-charts";
import type { useAnalytics } from "@/features/analytics/hooks/use-analytics";
import { analyticsRequestHref } from "@/features/analytics/utils/request-href";
import type { UsageBreakdownDimension, UsageBreakdownRow, UsageBreakdownSort } from "@/features/usage/types/usage.types";

type AnalyticsModel = ReturnType<typeof useAnalytics>;
type ChartSpec = { title: string; note: string; metric: RankMetric; sort: UsageBreakdownSort };
const filterKeys: Partial<Record<UsageBreakdownDimension, "user_id" | "key_id" | "model_id" | "connection_id">> = { key: "key_id", user: "user_id", model: "model_id", connection: "connection_id" };

function RankingTable({ model, dimension, rows, hasMore }: { model: AnalyticsModel; dimension: UsageBreakdownDimension; rows: UsageBreakdownRow[]; hasMore: boolean }) {
  const filterKey = filterKeys[dimension];
  return <details className="analytics-table-disclosure">
    <summary>Explore {dimension === "connection" ? "providers" : dimension === "key" ? "API keys" : dimension === "state" ? "outcomes" : dimension === "mode" ? "response modes" : `${dimension}s`} in a table</summary>
    <div className="table-wrap" tabIndex={0} role="region" aria-label={`Usage breakdown by ${dimension}`}>
      <table className="analytics-table">
        <thead><tr><th>{dimension === "connection" ? "Provider" : dimension === "key" ? "API key" : dimension === "user" ? "User" : dimension === "mode" ? "Response mode" : dimension}</th><th>Requests</th><th>Failed requests</th><th>Error rate</th><th>Failed attempts</th><th>Tokens</th><th>Known spend</th><th>Gateway p95</th><th>First byte p95</th><th>Explore</th></tr></thead>
        <tbody>{rows.map((row) => <tr key={row.id}>
          <td><strong>{row.label}</strong></td>
          <td>{row.requests.toLocaleString()}</td>
          <td>{row.failed_requests.toLocaleString()}</td>
          <td>{row.successful_requests + row.failed_requests ? `${row.error_rate_percent.toFixed(1)}%` : "—"}</td>
          <td>{row.failed_attempts.toLocaleString()}</td>
          <td>{(row.input_tokens + row.output_tokens).toLocaleString()}</td>
          <td>${row.known_cost_usd}{row.unknown_attempts > 0 && <small className="analytics-unknown"> · {row.unknown_attempts} unpriced</small>}</td>
          <td>{row.p95_gateway_duration_ms !== null ? rankFormat(row.p95_gateway_duration_ms, "p95") : "—"}</td>
          <td>{row.p95_first_byte_ms !== null ? rankFormat(row.p95_first_byte_ms, "first_byte") : "—"}</td>
          <td><div className="analytics-row-actions">
            {filterKey && row.id && <button type="button" onClick={() => model.drillDown(filterKey, row.id)}>Filter charts</button>}
            {row.id && <Link href={analyticsRequestHref(model.filters, model.usage, dimension, row.id)}>Requests</Link>}
          </div></td>
        </tr>)}</tbody>
      </table>
    </div>
    {hasMore && model.rankings[`${dimension}:requests`]?.next_offset !== null && <Button type="button" variant="outline" disabled={model.loadingMore === `${dimension}:requests`} onClick={() => model.loadMore(dimension)}>Load more</Button>}
    <p className="analytics-caveat">Totals include retained accounting and attempts started in this range. Request links show only details still retained for requests started in this range.</p>
  </details>;
}

export function AnalyticsBreakdownSection({ model, dimension, title, description, charts }: { model: AnalyticsModel; dimension: UsageBreakdownDimension; title: string; description: string; charts: ChartSpec[] }) {
  const requestRows = model.rankings[`${dimension}:requests`]?.data ?? [];
  return <section className="analytics-section" aria-label={title}>
    <div className="analytics-section-heading"><div><h2>{title}</h2><p>{description}</p></div><span>{requestRows.length ? `${requestRows.length} shown` : "No activity"}</span></div>
    <div className="analytics-chart-grid">
      {charts.map(({ title: chartTitle, note, metric, sort }) => {
        const breakdown = model.rankings[`${dimension}:${sort}`];
        return <Card className="panel chart-panel analytics-chart-card" key={chartTitle}>
          <h3>{chartTitle}</h3><p className="chart-note">{note}</p>
          <RankedChart rows={breakdown?.data ?? []} metric={metric} label={chartTitle} />
          {breakdown?.has_more && <p className="analytics-chart-footnote">Top 20 groups by {sort.replaceAll("_", " ")}.</p>}
        </Card>;
      })}
    </div>
    {requestRows.length > 0 && <RankingTable model={model} dimension={dimension} rows={requestRows} hasMore={model.rankings[`${dimension}:requests`]?.has_more ?? false} />}
  </section>;
}
