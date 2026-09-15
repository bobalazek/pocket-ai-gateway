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
});
