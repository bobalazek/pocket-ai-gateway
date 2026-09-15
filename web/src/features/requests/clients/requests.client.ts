import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { requestSearch } from "@/features/requests/utils/requests.utils";
import { gatewayTransport } from "@/lib/api-client";

export const requestsClient = {
  list: (filters: RequestFilters = {}, signal?: AbortSignal) => {
    const query = requestSearch(filters);
    return gatewayTransport.request<{ data: GatewayRequest[]; next_cursor: string; has_more: boolean }>(`/api/v1/requests${query ? `?${query}` : ""}`, { signal });
  },
};
