import { afterEach, describe, expect, it, vi } from "vitest";

import { auditClient } from "@/features/audit/clients/audit.client";
import { gatewayTransport } from "@/lib/api-client";

afterEach(() => vi.restoreAllMocks());

describe("auditClient", () => {
  it("serializes typed audit filters", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    await auditClient.list({ action: "provider.update", from: "2026-01-01T00:00:00Z" });
    expect(request.mock.calls[0][0]).toBe("/api/v1/admin/audit?action=provider.update&from=2026-01-01T00%3A00%3A00Z");
  });
});
