import { afterEach, describe, expect, it, vi } from "vitest";

import { requestsClient } from "@/features/requests/clients/requests.client";
import { gatewayTransport } from "@/lib/api-client";

afterEach(() => vi.restoreAllMocks());

describe("requestsClient", () => {
  it("serializes the request detail filter through the shared transport", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({ data: [], next_cursor: "", has_more: false });
    const controller = new AbortController();

    await requestsClient.list({ request_id: "req one", dialect: "openai" }, controller.signal);

    expect(request).toHaveBeenCalledWith("/api/v1/requests?request_id=req+one&dialect=openai", { signal: controller.signal });
  });
});
