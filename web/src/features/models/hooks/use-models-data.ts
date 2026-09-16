"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { CatalogCandidate, CatalogModel, CatalogState, PublicModel, RouteStrategy, RouteTargetInput } from "@/features/models/types/models.types";
import type { UpstreamModel } from "@/features/providers/types/providers.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

const failureText = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? error.message : fallback;

export function useModelsData(canManage: boolean, search: string) {
  const [models, setModels] = useState<(CatalogModel | PublicModel)[]>([]);
  const [targets, setTargets] = useState<UpstreamModel[]>([]);
  const [availableTargets, setAvailableTargets] = useState<Record<string, UpstreamModel[]>>({});
  const [selectedTarget, setSelectedTarget] = useState("");
  const [routes, setRoutes] = useState<Record<string, RouteTargetInput[]>>({});
  const [selectedStrategies, setSelectedStrategies] = useState<Record<string, RouteStrategy>>({});
  const [catalog, setCatalog] = useState<CatalogCandidate[]>([]);
  const [catalogState, setCatalogState] = useState<CatalogState | null>(null);
  const [catalogCursor, setCatalogCursor] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setBusy(true);
    try {
      if (!canManage) {
        const result = await pocketAIGatewayAdmin.models.publicModels(search);
        if (current !== generation.current) return;
        setModels(result.data);
        setTargets([]);
        setAvailableTargets({});
        setRoutes({});
        setCatalog([]);
        setCatalogState(null);
        setCatalogCursor("");
      } else {
        const [managed, catalogResult] = await Promise.all([
          pocketAIGatewayAdmin.models.managedModels(search),
          pocketAIGatewayAdmin.models.catalog(),
        ]);
        if (current !== generation.current) return;
        setModels(managed.data);
        setTargets(managed.publish_targets);
        setRoutes(managed.routes);
        setAvailableTargets(managed.available_targets);
        setCatalog(catalogResult.data);
        setCatalogState(catalogResult.state);
        setCatalogCursor(catalogResult.next_cursor);
      }
      setSelectedStrategies({});
      setError("");
    } catch (failure) {
      if (current === generation.current) setError(failureText(failure, "Models are unavailable"));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }, [canManage, search]);

  useEffect(() => { void load(); }, [load]);

  const targetsByID = useMemo(() => Object.fromEntries(targets.map((target) => [target.id, target])), [targets]);
  const publishCapabilities = targetsByID[selectedTarget]?.capability_details ?? [];
  const selectStrategy = (modelID: string, strategy: RouteStrategy) => setSelectedStrategies((current) => ({ ...current, [modelID]: strategy }));

  const loadMoreCatalog = useCallback(async () => {
    if (!catalogCursor) return;
    const current = generation.current;
    setBusy(true);
    try {
      const page = await pocketAIGatewayAdmin.models.catalog(catalogCursor);
      if (current !== generation.current) return;
      setCatalog((items) => [...items, ...page.data]);
      setCatalogCursor(page.next_cursor);
      setError("");
    } catch (failure) {
      if (current === generation.current) setError(failureText(failure, "The next catalog page could not be loaded"));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }, [catalogCursor]);

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
    catalogCursor,
    setCatalogState,
    publishCapabilities,
    error,
    busy,
    load,
    loadMoreCatalog,
  };
}
