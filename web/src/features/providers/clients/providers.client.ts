import type { ProviderAdapter, ProviderAdapterScript, ProviderConnection, ProviderPreset, UpstreamModel } from "@/features/providers/types/providers.types";
import { gatewayTransport } from "@/lib/api-client";

type ConnectionInput = { name: string; adapter: ProviderConnection["adapter"]; base_url: string; enabled: boolean; allow_private_network: boolean; timeout_ms: number; preset?: string };

export const providersClient = {
  presets: () => gatewayTransport.request<{ data: ProviderPreset[]; adapters: ProviderAdapter[] }>("/api/v1/provider-presets"),
  connections: () => gatewayTransport.request<{ data: ProviderConnection[] }>("/api/v1/connections"),
  createConnection: (input: ConnectionInput) => gatewayTransport.request<{ connection: ProviderConnection }>("/api/v1/connections", { method: "POST", body: input }),
  updateConnection: (connection: ProviderConnection, input: ConnectionInput) => gatewayTransport.request<{ connection: ProviderConnection }>(`/api/v1/connections/${encodeURIComponent(connection.id)}`, { method: "PATCH", revision: connection.revision, body: input }),
  putCredential: (id: string, input: { credential?: string; external_ref?: string }) => gatewayTransport.request<void>(`/api/v1/connections/${encodeURIComponent(id)}/credential`, { method: "PUT", body: input }),
  createUpstreamModel: (connectionID: string, input: { upstream_id: string; capabilities: string[] }) => gatewayTransport.request<{ model: UpstreamModel }>(`/api/v1/connections/${encodeURIComponent(connectionID)}/models`, { method: "POST", body: input }),
  adapterScript: (connectionID: string) => gatewayTransport.request<{ adapter_script: ProviderAdapterScript }>(`/api/v1/connections/${encodeURIComponent(connectionID)}/adapter-script`),
  putAdapterScript: (connection: ProviderConnection, input: { request_script: string; response_script: string }) => gatewayTransport.request<{ adapter_script: ProviderAdapterScript }>(`/api/v1/connections/${encodeURIComponent(connection.id)}/adapter-script`, { method: "PUT", revision: connection.revision, body: input }),
  deleteAdapterScript: (connection: ProviderConnection) => gatewayTransport.request<void>(`/api/v1/connections/${encodeURIComponent(connection.id)}/adapter-script`, { method: "DELETE", revision: connection.revision }),
};
