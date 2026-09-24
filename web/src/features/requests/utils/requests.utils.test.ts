import { describe, expect, it } from "vitest";

import { readRequestFilters, requestListFilters, requestSearch } from "@/features/requests/utils/requests.utils";

describe("request URL state", () => {
  it("round-trips a static request detail deep link", () => {
    const filters = readRequestFilters("?request_id=req_1&model_id=assistant&ignored=value");

    expect(filters).toEqual({ request_id: "req_1", model_id: "assistant" });
    expect(requestSearch(filters)).toBe("request_id=req_1&model_id=assistant");
    expect(requestListFilters({ ...filters, cursor: "next" })).toEqual({ model_id: "assistant" });
  });

  it("preserves analytics drill-down filters in request links", () => {
    const filters = readRequestFilters("?key_id=key_1&connection_id=con_2&state=failed&from=2026-09-01T00%3A00%3A00Z&to=2026-09-02T00%3A00%3A00Z");
    expect(filters).toEqual({ key_id: "key_1", connection_id: "con_2", state: "failed", from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z" });
    expect(readRequestFilters(`?${requestSearch(filters)}`)).toEqual(filters);
  });
});
