import { Area, AreaChart, Bar, BarChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";

import { ChartContainer } from "@/components/ui/chart";
import type { UsageBreakdownRow, UsagePoint } from "@/features/usage/types/usage.types";
import { formatChartUSD, formatChartUSDAxis } from "@/features/usage/utils/usage.utils";

export type RankMetric = "requests" | "tokens" | "spend" | "failures" | "failed_attempts" | "p95" | "first_byte" | "tools" | "cache_reads" | "unknown";

export function rankValue(row: UsageBreakdownRow, metric: RankMetric) {
  if (metric === "tokens") return row.input_tokens + row.output_tokens;
  if (metric === "spend") return Number(row.known_cost_usd);
  if (metric === "failures") return row.failed_requests;
  if (metric === "failed_attempts") return row.failed_attempts;
  if (metric === "p95") return row.p95_gateway_duration_ms ?? 0;
  if (metric === "first_byte") return row.p95_first_byte_ms ?? 0;
  if (metric === "tools") return row.response_tool_calls;
  if (metric === "cache_reads") return row.cache_read_input_tokens;
  if (metric === "unknown") return row.unknown_attempts;
  return row.requests;
}

export function rankFormat(value: number, metric: RankMetric) {
  if (metric === "spend") return formatChartUSD(value);
  if (metric === "p95" || metric === "first_byte") return `${Math.round(value).toLocaleString()} ms`;
  return value.toLocaleString();
}

export function RankedChart({ rows, metric, label }: { rows: UsageBreakdownRow[]; metric: RankMetric; label: string }) {
  const data = rows.filter((row) => (metric !== "p95" || row.finished_requests > 0) && (metric !== "first_byte" || row.p95_first_byte_ms !== null))
    .map((row) => {
      const name = row.label.split(" · ")[0];
      return { ...row, value: rankValue(row, metric), shortLabel: name.length > 18 ? `${name.slice(0, 17)}…` : name };
    })
    .sort((left, right) => right.value - left.value || left.id.localeCompare(right.id))
    .slice(0, 8);
  if (!data.length) return <p className="empty-copy">No measured values in this period.</p>;
  if (data.every((item) => item.value === 0)) return <p className="empty-copy">All values are zero in this period.</p>;
  return <>
    <ChartContainer label={label} className="analytics-rank-chart">
      <BarChart responsive width="100%" height="100%" data={data} layout="vertical" margin={{ left: 4, right: 12 }}>
        <CartesianGrid horizontal={false} />
        <XAxis type="number" tickFormatter={(value: number) => metric === "spend" ? formatChartUSDAxis(value) : metric === "p95" || metric === "first_byte" ? `${value}ms` : value.toLocaleString()} />
        <YAxis type="category" dataKey="shortLabel" width={126} tickLine={false} />
        <Tooltip formatter={(value) => rankFormat(Number(value), metric)} labelFormatter={(_, payload) => String(payload?.[0]?.payload?.label ?? "")} />
        <Bar dataKey="value" name={label} fill="var(--accent)" radius={[0, 4, 4, 0]} isAnimationActive={false} />
      </BarChart>
    </ChartContainer>
    <p className="chart-summary">Highest shown: <strong>{data[0].label}</strong> · {rankFormat(data[0].value, metric)}</p>
  </>;
}

export function CacheTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily cache reads and writes in tokens">
    <AreaChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={(value: string) => value.slice(5)} />
      <YAxis width={56} />
      <Tooltip />
      <Area type="monotone" dataKey="cache_read_input_tokens" name="Cache reads" stroke="var(--accent)" fill="var(--accent-soft)" isAnimationActive={false} />
      <Area type="monotone" dataKey="cache_creation_input_tokens" name="Cache writes" stroke="var(--text)" fill="var(--chart-ink-soft)" isAnimationActive={false} />
    </AreaChart>
  </ChartContainer>;
}

export function UnknownTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily attempts needing usage or cost review">
    <BarChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={(value: string) => value.slice(5)} />
      <YAxis width={38} allowDecimals={false} />
      <Tooltip />
      <Bar dataKey="unknown_attempts" name="Needs review" fill="var(--text)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ChartContainer>;
}

export function FailureTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily failed gateway requests">
    <BarChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={(value: string) => value.slice(5)} />
      <YAxis width={38} allowDecimals={false} />
      <Tooltip />
      <Bar dataKey="failed_requests" name="Failed requests" fill="var(--danger)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ChartContainer>;
}

export function WebSearchTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily hosted web-search calls">
    <BarChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={(value: string) => value.slice(5)} />
      <YAxis width={38} allowDecimals={false} />
      <Tooltip />
      <Bar dataKey="web_search_calls" name="Web-search calls" fill="var(--accent)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ChartContainer>;
}
