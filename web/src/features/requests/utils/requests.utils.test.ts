import { describe, expect, it } from "vitest";

import { readRequestFilters, requestListFilters, requestSearch } from "@/features/requests/utils/requests.utils";

describe("request URL state", () => {
  it("round-trips a static request detail deep link", () => {
    const filters = readRequestFilters("?request_id=req_1&model_id=assistant&ignored=value");

    expect(filters).toEqual({ request_id: "req_1", model_id: "assistant" });
    expect(requestSearch(filters)).toBe("request_id=req_1&model_id=assistant");
    expect(requestListFilters({ ...filters, cursor: "next" })).toEqual({ model_id: "assistant" });
  });
});
