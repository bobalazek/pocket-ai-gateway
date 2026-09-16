"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { CatalogCandidate, CatalogModel, CatalogState, PublicModel, RouteStrategy, RouteTargetInput } from "@/features/models/types/models.types";
import type { UpstreamModel } from "@/features/providers/types/providers.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

const failureText = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? error.message : fallback;

export function useModelsData(canManage: boolean) {
  const [models, setModels] = useState<(CatalogModel | PublicModel)[]>([]);
  const [targets, setTargets] = useState<UpstreamModel[]>([]);
  const [availableTargets, setAvailableTargets] = useState<Record<string, UpstreamModel[]>>({});
  const [selectedTarget, setSelectedTarget] = useState("");
  const [routes, setRoutes] = useState<Record<string, RouteTargetInput[]>>({});
  const [selectedStrategies, setSelectedStrategies] = useState<Record<string, RouteStrategy>>({});
  const [catalog, setCatalog] = useState<CatalogCandidate[]>([]);
  const [catalogState, setCatalogState] = useState<CatalogState | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setBusy(true);
    try {
      if (!canManage) {
        const result = await pocketAIGatewayAdmin.models.publicModels();
        if (current !== generation.current) return;
        setModels(result.data);
        setTargets([]);
        setAvailableTargets({});
        setRoutes({});
        setCatalog([]);
        setCatalogState(null);
      } else {
        const [managed, connections, catalogResult] = await Promise.all([
          pocketAIGatewayAdmin.models.managedModels(),
          pocketAIGatewayAdmin.models.connections(),
          pocketAIGatewayAdmin.models.catalog(),
        ]);
        const [upstreamPages, routePages] = await Promise.all([
          Promise.all(connections.data.map((item) => pocketAIGatewayAdmin.models.upstreamModels(item.id))),
          Promise.all(managed.data.map((model) => pocketAIGatewayAdmin.models.routeConfig(model.id))),
        ]);
        if (current !== generation.current) return;
        setModels(managed.data);
        setTargets(upstreamPages.flatMap((page) => page.data));
        setRoutes(Object.fromEntries(routePages.map((page) => [page.model.id, page.targets])));
        setAvailableTargets(Object.fromEntries(routePages.map((page) => [page.model.id, page.available_targets])));
        setCatalog(catalogResult.data);
        setCatalogState(catalogResult.state);
      }
      setSelectedStrategies({});
      setError("");
    } catch (failure) {
      if (current === generation.current) setError(failureText(failure, "Models are unavailable"));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }, [canManage]);

  useEffect(() => { void load(); }, [load]);

  const targetsByID = useMemo(() => Object.fromEntries(targets.map((target) => [target.id, target])), [targets]);
  const publishCapabilities = targetsByID[selectedTarget]?.capability_details ?? [];
  const selectStrategy = (modelID: string, strategy: RouteStrategy) => setSelectedStrategies((current) => ({ ...current, [modelID]: strategy }));

  return {
    models,
    targets,
    availableTargets,
    selectedTarget,
    setSelectedTarget,
    selectedStrategies,
    selectStrategy,
    routes,
    catalog,
    catalogState,
    setCatalogState,
    publishCapabilities,
    error,
    busy,
    load,
  };
}
