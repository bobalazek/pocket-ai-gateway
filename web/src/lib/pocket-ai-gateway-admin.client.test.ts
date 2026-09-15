import { describe, expect, it, vi } from "vitest";

import { keysClient } from "@/features/keys/clients/keys.client";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { gatewayTransport } from "@/lib/api-client";

describe("pocketAIGatewayAdmin", () => {
  it("exposes every management namespace through one facade", () => {
    expect(Object.keys(pocketAIGatewayAdmin).sort()).toEqual([
      "account", "audit", "auth", "keys", "models", "playground",
      "providers", "requests", "settings", "status", "usage", "users",
    ]);
  });

  it("delegates a namespace call to its feature client", async () => {
    const request = vi.spyOn(gatewayTransport, "request").mockResolvedValue({ data: [], next_cursor: "", has_more: false });
    expect(pocketAIGatewayAdmin.keys).toBe(keysClient);
    await pocketAIGatewayAdmin.keys.list("next");
    expect(request).toHaveBeenCalledWith("/api/v1/keys?cursor=next");
    request.mockRestore();
  });
});
