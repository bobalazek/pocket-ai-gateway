"use client";

import { useEffect, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { CatalogCandidate, CatalogModel, CatalogState, PublicModel, RoutePlan, RoutePreviewInput, RouteStrategy, RouteTargetInput } from "@/features/models/types/models.types";
import type { UpstreamModel } from "@/features/providers/types/providers.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

const message = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? error.message : fallback;

export function useModels() {
  const user = useGatewayUser(); const manager = user?.role === "owner" || user?.role === "admin";
  const [models, setModels] = useState<(CatalogModel | PublicModel)[]>([]);
  const [targets, setTargets] = useState<UpstreamModel[]>([]);
  const [availableTargets, setAvailableTargets] = useState<Record<string, UpstreamModel[]>>({});
  const [selectedTarget, setSelectedTarget] = useState("");
  const [routes, setRoutes] = useState<Record<string, RouteTargetInput[]>>({});
  const [selectedStrategies, setSelectedStrategies] = useState<Record<string, RouteStrategy>>({});
  const [previews, setPreviews] = useState<Record<string, { route: RoutePlan; input: RoutePreviewInput }>>({});
  const [catalog, setCatalog] = useState<CatalogCandidate[]>([]);
  const [catalogState, setCatalogState] = useState<CatalogState | null>(null);
  const [error, setError] = useState(""); const [busy, setBusy] = useState(false);

  async function load() {
    try {
      if (manager) {
        const [managed, connections, catalogResult] = await Promise.all([pocketAIGatewayAdmin.models.managedModels(), pocketAIGatewayAdmin.models.connections(), pocketAIGatewayAdmin.models.catalog()]);
        const upstreamPages = await Promise.all(connections.data.map((item) => pocketAIGatewayAdmin.models.upstreamModels(item.id)));
        const routePages = await Promise.all(managed.data.map((model) => pocketAIGatewayAdmin.models.routeConfig(model.id)));
        setModels(managed.data); setTargets(upstreamPages.flatMap((page) => page.data)); setCatalog(catalogResult.data); setCatalogState(catalogResult.state); setSelectedStrategies({});
        setRoutes(Object.fromEntries(routePages.map((page) => [page.model.id, page.targets])));
        setAvailableTargets(Object.fromEntries(routePages.map((page) => [page.model.id, page.available_targets])));
      } else setModels((await pocketAIGatewayAdmin.models.publicModels()).data);
      setError("");
    } catch (failure) { setError(message(failure, "Models are unavailable")); }
  }
  useEffect(() => { void load(); }, [manager]);

  async function create(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const element = event.currentTarget; const form = new FormData(element); setBusy(true); try { await pocketAIGatewayAdmin.models.createPublicModel({ id: String(form.get("id")), label: String(form.get("label")), description: String(form.get("description")), target_model_id: String(form.get("target_model_id")), capabilities: form.getAll("capabilities").map(String) }); element.reset(); setSelectedTarget(""); await load(); } catch (failure) { setError(message(failure, "Model could not be published")); } finally { setBusy(false); } }
  async function saveRoute(event: FormEvent<HTMLFormElement>, model: PublicModel) { event.preventDefault(); const form = new FormData(event.currentTarget); const selectedIDs = new Set(form.getAll("target").map(String)); const selected = (availableTargets[model.id] ?? []).filter((target) => selectedIDs.has(target.id)).map((target) => ({ upstream_model_id: target.id, priority: Number(form.get(`priority:${target.id}`)), weight: Number(form.get(`weight:${target.id}`)), enabled: true })); setBusy(true); try { await pocketAIGatewayAdmin.models.updateRoute(model, { strategy: String(form.get("strategy")) as RouteStrategy, free_only: form.get("free_only") === "on", targets: selected }); await load(); } catch (failure) { setError(message(failure, "Route could not be saved")); } finally { setBusy(false); } }
  async function preview(event: FormEvent<HTMLFormElement>, model: PublicModel) { event.preventDefault(); const form = new FormData(event.currentTarget); const input = { operation: String(form.get("operation")), streaming: form.get("streaming") === "on", estimated_input_tokens: Number(form.get("estimated_input_tokens")), estimated_output_tokens: Number(form.get("estimated_output_tokens")) }; setBusy(true); try { const result = await pocketAIGatewayAdmin.models.previewRoute(model.id, input); setPreviews((current) => ({ ...current, [model.id]: { route: result.route, input } })); } catch (failure) { setError(message(failure, "Route preview failed")); } finally { setBusy(false); } }
  async function saveCatalog(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); setBusy(true); try { const result = await pocketAIGatewayAdmin.models.configureCatalog({ source_url: String(form.get("source_url")), refresh_enabled: form.get("refresh_enabled") === "on", refresh_interval_hours: Number(form.get("refresh_interval_hours")) }); setCatalogState(result.state); } catch (failure) { setError(message(failure, "Catalog settings could not be saved")); } finally { setBusy(false); } }
  async function refreshCatalog() { setBusy(true); try { await pocketAIGatewayAdmin.models.refreshCatalog(); await load(); } catch (failure) { setError(message(failure, "Catalog refresh failed")); } finally { setBusy(false); } }
  function selectStrategy(modelID: string, strategy: RouteStrategy) { setSelectedStrategies((current) => ({ ...current, [modelID]: strategy })); }

  const publishCapabilities = targets.find((target) => target.id === selectedTarget)?.capability_details ?? [];
  return { manager, models, targets, availableTargets, selectedTarget, setSelectedTarget, selectedStrategies, selectStrategy, routes, previews, catalog, catalogState, error, busy, create, saveRoute, preview, saveCatalog, refreshCatalog, publishCapabilities };
}
