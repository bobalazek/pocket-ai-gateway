import { describe, expect, it } from "vitest";

import { analyticsRequestHref } from "@/features/analytics/utils/request-href";

describe("analytics request drill-down", () => {
  it("preserves the cohort and shared time window when selecting a row", () => {
    const href = analyticsRequestHref(
      { key_id: "key_one", model_id: "model_old", user_id: "user_one" },
      { from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z" },
      "model", "model_new",
    );
    const query = new URLSearchParams(href.split("?")[1]);
    expect(Object.fromEntries(query)).toEqual({
      key_id: "key_one", model_id: "model_new", user_id: "user_one",
      from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z",
    });
  });
});
