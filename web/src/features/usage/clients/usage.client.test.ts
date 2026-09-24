import { afterEach, describe, expect, it, vi } from "vitest";

import { usageClient } from "@/features/usage/clients/usage.client";
import type { LimitPolicy } from "@/features/usage/types/usage.types";
import { gatewayTransport } from "@/lib/api-client";

afterEach(() => vi.restoreAllMocks());

describe("usageClient", () => {
  it("uses the usage namespace and revision precondition", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    await usageClient.summary({ from: "2026-01-01T00:00:00Z", to: "2026-02-01T00:00:00Z" });
    await usageClient.updatePolicy({ id: "pol_test", revision: 3 } as LimitPolicy, { limit_units: 10, limit_usd: "", enabled: true });
    expect(request.mock.calls[0][0]).toBe("/api/v1/usage?from=2026-01-01T00%3A00%3A00Z&to=2026-02-01T00%3A00%3A00Z");
    expect(request.mock.calls[1]).toEqual(["/api/v1/admin/policies/pol_test", expect.objectContaining({ revision: 3 })]);
  });

  it("requests a server-ranked API-key spend breakdown with the shared filters", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    await usageClient.breakdown("key", { from: "2026-09-01T00:00:00Z", key_id: "key_1" }, "known_cost", 20);
    expect(request.mock.calls[0][0]).toBe("/api/v1/usage/breakdown?from=2026-09-01T00%3A00%3A00Z&key_id=key_1&dimension=key&sort=known_cost&offset=20");
  });

  it("sends cache-read and web-search pricing with the weekly UTC window", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    const input = {
      connection_id: "conn_1",
      model_id: "assistant",
      input_usd_per_million: "1",
      cache_read_usd_per_million: "0.1",
      web_search_usd_per_call: "0.01",
      output_usd_per_million: "3",
      source: "provider pricing",
      effective_from: "2026-09-15T00:00:00Z",
      effective_to: "",
      weekly_start_minute_utc: 540,
      weekly_end_minute_utc: 6_780,
    };

    await usageClient.createPrice(input);

    expect(request).toHaveBeenCalledWith("/api/v1/admin/prices", { method: "POST", body: input });
  });
});
