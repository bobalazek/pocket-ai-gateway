import { describe, expect, it } from "vitest";

import { formatWeeklyPriceWindow, parseWeeklyPriceWindow } from "@/features/usage/utils/pricing.utils";

describe("weekly price windows", () => {
  it("converts UTC weekday and time fields without using the browser timezone", () => {
    expect(parseWeeklyPriceWindow("1", "08:30", "4", "17:45")).toEqual({
      weekly_start_minute_utc: 1_950,
      weekly_end_minute_utc: 6_825,
    });
    expect(formatWeeklyPriceWindow(1_950, 6_825)).toBe("Tuesday 08:30–Friday 17:45 UTC");
  });

  it("supports an all-week price and the end-of-week boundary", () => {
    expect(parseWeeklyPriceWindow("", "", "", "")).toEqual({
      weekly_start_minute_utc: null,
      weekly_end_minute_utc: null,
    });
    expect(formatWeeklyPriceWindow(null, null)).toBe("All week");
    expect(parseWeeklyPriceWindow("6", "09:00", "7", "00:00")).toEqual({
      weekly_start_minute_utc: 9_180,
      weekly_end_minute_utc: 10_080,
    });
  });

  it("rejects incomplete, reversed, and invalid end-of-week fields", () => {
    expect(() => parseWeeklyPriceWindow("1", "08:30", "", "")).toThrow("both a start and end");
    expect(() => parseWeeklyPriceWindow("4", "17:45", "1", "08:30")).toThrow("end after it starts");
    expect(() => parseWeeklyPriceWindow("6", "09:00", "7", "00:01")).toThrow("valid weekly end");
  });
});
