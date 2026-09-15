import type { CatalogCandidate, CatalogModel, CatalogState, PublicModel, RoutePlan, RoutePreviewInput, RouteStrategy, RouteTargetInput } from "@/features/models/types/models.types";
import type { ProviderConnection, UpstreamModel } from "@/features/providers/types/providers.types";
import { gatewayTransport } from "@/lib/api-client";

export const modelsClient = {
  connections: () => gatewayTransport.request<{ data: ProviderConnection[] }>("/api/v1/connections"),
  upstreamModels: (connectionID: string) => gatewayTransport.request<{ data: UpstreamModel[] }>(`/api/v1/connections/${encodeURIComponent(connectionID)}/models`),
  publicModels: () => gatewayTransport.request<{ data: CatalogModel[] }>("/api/v1/models"),
  createPublicModel: (input: { id: string; label: string; description: string; target_model_id: string; capabilities: string[] }) => gatewayTransport.request<{ model: PublicModel }>("/api/v1/models", { method: "POST", body: input }),
  managedModels: () => gatewayTransport.request<{ data: PublicModel[] }>("/api/v1/admin/models"),
  routeConfig: (id: string) => gatewayTransport.request<{ model: PublicModel; targets: RouteTargetInput[] }>(`/api/v1/admin/models/${encodeURIComponent(id)}/route`),
  updateRoute: (model: PublicModel, input: { strategy: RouteStrategy; free_only: boolean; targets: RouteTargetInput[] }) => gatewayTransport.request<{ model: PublicModel }>(`/api/v1/admin/models/${encodeURIComponent(model.id)}/route`, { method: "PUT", revision: model.revision, body: input }),
  previewRoute: (id: string, input: RoutePreviewInput) => gatewayTransport.request<{ route: RoutePlan }>(`/api/v1/admin/models/${encodeURIComponent(id)}/route-preview`, { method: "POST", body: input }),
  catalog: () => gatewayTransport.request<{ data: CatalogCandidate[]; state: CatalogState }>("/api/v1/admin/catalog"),
  configureCatalog: (input: { source_url: string; refresh_enabled: boolean; refresh_interval_hours: number }) => gatewayTransport.request<{ state: CatalogState }>("/api/v1/admin/catalog", { method: "PUT", body: input }),
  refreshCatalog: () => gatewayTransport.request<{ state: CatalogState }>("/api/v1/admin/catalog/refresh", { method: "POST" }),
};
