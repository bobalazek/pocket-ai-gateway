import type { UsagePoint, UsageSummary } from "@/features/usage/types/usage.types";

const dayMilliseconds = 86_400_000;

export function dailyPoints(summary: UsageSummary): UsagePoint[] {
  const from = Date.parse(summary.from);
  const to = Date.parse(summary.to);
  if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) return summary.points;
  const recorded = new Map(summary.points.map((point) => [point.date, point]));
  const points: UsagePoint[] = [];
  const start = Math.floor(from / dayMilliseconds) * dayMilliseconds;
  for (let day = start; day < to && points.length <= 366; day += dayMilliseconds) {
    const date = new Date(day).toISOString().slice(0, 10);
    points.push(recorded.get(date) ?? {
      date, requests: 0, successful_requests: 0, failed_requests: 0, failed_attempts: 0, error_rate_percent: 0,
      input_tokens: 0, output_tokens: 0, cache_creation_input_tokens: 0,
      cache_read_input_tokens: 0, cache_creation_5m_input_tokens: 0, cache_creation_1h_input_tokens: 0,
      web_search_calls: 0, known_cost_usd: "0", unknown_attempts: 0,
    });
  }
  return points;
}
