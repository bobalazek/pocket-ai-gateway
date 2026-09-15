import type { AuditEvent, AuditFilters } from "@/features/audit/types/audit.types";
import { gatewayTransport } from "@/lib/api-client";

export const auditClient = {
  list: (filters: AuditFilters = {}, signal?: AbortSignal) => {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
    return gatewayTransport.request<{ data: AuditEvent[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/audit${query.size ? `?${query}` : ""}`, { signal });
  },
};
