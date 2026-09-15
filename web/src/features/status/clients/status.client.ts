import { GatewayAPIError, gatewayTransport } from "@/lib/api-client";

export const statusClient = {
  health: (signal?: AbortSignal) => gatewayTransport.request<{ status: "ok" }>("/healthz", { signal }),
  readiness: async (signal?: AbortSignal) => {
    try {
      await gatewayTransport.request<unknown>("/readyz", { signal });
      return { ready: true };
    } catch (error) {
      if (error instanceof GatewayAPIError && error.status === 503) return { ready: false };
      throw error;
    }
  },
};
