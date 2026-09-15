import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { gatewayTransport } from "@/lib/api-client";

export const requestsClient = {
  list: (filters: RequestFilters = {}, signal?: AbortSignal) => {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
    return gatewayTransport.request<{ data: GatewayRequest[]; next_cursor: string; has_more: boolean }>(`/api/v1/requests${query.size ? `?${query}` : ""}`, { signal });
  },
};
