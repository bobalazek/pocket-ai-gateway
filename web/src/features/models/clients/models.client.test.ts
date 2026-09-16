import { afterEach, describe, expect, it, vi } from "vitest";

import { modelsClient } from "@/features/models/clients/models.client";
import type { PublicModel } from "@/features/models/types/models.types";
import { gatewayTransport } from "@/lib/api-client";

afterEach(() => vi.restoreAllMocks());

describe("modelsClient", () => {
  it("uses server-side model search and encoded management paths", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    await modelsClient.managedModels(" assistant/model ");
    await modelsClient.updateRoute({ id: "assistant", revision: 4 } as PublicModel, { strategy: "ordered_fallback", free_only: false, targets: [] });
    await modelsClient.catalog("next/page");
    expect(request.mock.calls[0][0]).toBe("/api/v1/admin/models?q=assistant%2Fmodel");
    expect(request.mock.calls[1]).toEqual(["/api/v1/admin/models/assistant/route", expect.objectContaining({ revision: 4 })]);
    expect(request.mock.calls[2][0]).toBe("/api/v1/admin/catalog?cursor=next%2Fpage");
  });
});
