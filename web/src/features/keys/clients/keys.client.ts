import type { GatewayKey } from "@/features/keys/types/keys.types";
import { gatewayTransport } from "@/lib/api-client";

export const keysClient = {
  list: (cursor = "") => gatewayTransport.request<{ data: GatewayKey[]; next_cursor: string; has_more: boolean }>(`/api/v1/keys${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`),
  create: (input: { label: string; scopes: string[]; model_patterns: string[]; connection_ids: string[]; expires_at: string }) => gatewayTransport.request<{ key: GatewayKey; secret: string }>("/api/v1/keys", { method: "POST", body: input }),
  update: (id: string, revision: number, input: { label: string; state: "active" | "disabled"; scopes: string[]; model_patterns: string[]; connection_ids: string[]; expires_at: string }) => gatewayTransport.request<{ key: GatewayKey }>(`/api/v1/keys/${encodeURIComponent(id)}`, { method: "PATCH", body: input, revision }),
  rotate: (id: string, revision: number) => gatewayTransport.request<{ key: GatewayKey; secret: string }>(`/api/v1/keys/${encodeURIComponent(id)}/rotate`, { method: "POST", revision }),
  revoke: (id: string, revision: number) => gatewayTransport.request<void>(`/api/v1/keys/${encodeURIComponent(id)}`, { method: "DELETE", revision }),
};
