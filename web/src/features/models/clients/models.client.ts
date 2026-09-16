import type { CatalogModel, CatalogPage, CatalogSettingsInput, CatalogState, CreatePublicModelInput, ManagedModelsView, PublicModel, RoutePlan, RoutePreviewInput, UpdateRouteInput } from "@/features/models/types/models.types";
import { gatewayTransport } from "@/lib/api-client";

export const modelsClient = {
  publicModels: (search = "") => gatewayTransport.request<{ data: CatalogModel[] }>(`/api/v1/models${modelSearch(search)}`),
  createPublicModel: (input: CreatePublicModelInput) => gatewayTransport.request<{ model: PublicModel }>("/api/v1/models", { method: "POST", body: input }),
  managedModels: (search = "") => gatewayTransport.request<ManagedModelsView>(`/api/v1/admin/models${modelSearch(search)}`),
  updateRoute: (model: PublicModel, input: UpdateRouteInput) => gatewayTransport.request<{ model: PublicModel }>(`/api/v1/admin/models/${encodeURIComponent(model.id)}/route`, { method: "PUT", revision: model.revision, body: input }),
  previewRoute: (id: string, input: RoutePreviewInput) => gatewayTransport.request<{ route: RoutePlan }>(`/api/v1/admin/models/${encodeURIComponent(id)}/route-preview`, { method: "POST", body: input }),
  catalog: (cursor = "") => gatewayTransport.request<CatalogPage>(`/api/v1/admin/catalog${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`),
  configureCatalog: (input: CatalogSettingsInput) => gatewayTransport.request<{ state: CatalogState }>("/api/v1/admin/catalog", { method: "PUT", body: input }),
  refreshCatalog: () => gatewayTransport.request<{ state: CatalogState }>("/api/v1/admin/catalog/refresh", { method: "POST" }),
};

const modelSearch = (search: string) => search.trim() ? `?q=${encodeURIComponent(search.trim())}` : "";
