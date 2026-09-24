import { afterEach, describe, expect, it, vi } from "vitest";

import { providersClient } from "@/features/providers/clients/providers.client";
import type { ProviderConnection } from "@/features/providers/types/providers.types";
import { gatewayTransport } from "@/lib/api-client";

afterEach(() => vi.restoreAllMocks());

describe("providersClient", () => {
  it("lists models under the encoded connection path", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({ data: [] });
    await providersClient.upstreamModels("custom/provider");
    expect(request).toHaveBeenCalledWith("/api/v1/connections/custom%2Fprovider/models");
  });

  it("uses the encoded connection path and revision for adapter scripts", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({});
    const connection = { id: "custom/provider", revision: 7 } as ProviderConnection;
    await providersClient.putAdapterScript(connection, { request_script: "request", response_script: "response" });
    expect(request).toHaveBeenCalledWith("/api/v1/connections/custom%2Fprovider/adapter-script", expect.objectContaining({ method: "PUT", revision: 7 }));
  });
});
