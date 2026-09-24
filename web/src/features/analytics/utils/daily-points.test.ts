import { describe, expect, it } from "vitest";

import { dailyPoints } from "@/features/analytics/utils/daily-points";
import { emptyUsage } from "@/features/usage/utils/usage.utils";

describe("dailyPoints", () => {
  it("keeps recorded usage and fills inactive dates without changing totals", () => {
    const points = dailyPoints({
      ...emptyUsage,
      from: "2026-09-01T12:00:00Z", to: "2026-09-04T12:00:00Z",
      points: [{ date: "2026-09-02", requests: 3, successful_requests: 2, failed_requests: 1, failed_attempts: 1, error_rate_percent: 33.33, input_tokens: 12, output_tokens: 4, cache_creation_input_tokens: 0, cache_read_input_tokens: 2, cache_creation_5m_input_tokens: 0, cache_creation_1h_input_tokens: 0, web_search_calls: 0, known_cost_usd: "0.01", unknown_attempts: 0 }],
    });
    expect(points.map((point) => point.date)).toEqual(["2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04"]);
    expect(points.map((point) => point.requests)).toEqual([0, 3, 0, 0]);
    expect(points.map((point) => point.failed_requests)).toEqual([0, 1, 0, 0]);
    expect(points[1].known_cost_usd).toBe("0.01");
  });
});
