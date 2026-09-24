import { describe, expect, it } from "vitest";

import { formatChartUSD, formatChartUSDAxis } from "@/features/usage/utils/usage.utils";

describe("chart currency", () => {
  it("keeps nonzero nanodollar spend visible", () => {
    expect(formatChartUSD(0.000000001)).toBe("$0.000000001");
    expect(formatChartUSDAxis(0.000000001)).not.toBe("$0");
    expect(formatChartUSD(2.287798)).toBe("$2.287798");
  });
});
