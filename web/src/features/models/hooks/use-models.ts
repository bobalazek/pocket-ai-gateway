"use client";

import { useGatewayUser } from "@/components/setup-gate";
import { useModelsActions } from "@/features/models/hooks/use-models-actions";
import { useModelsData } from "@/features/models/hooks/use-models-data";

export function useModels() {
  const user = useGatewayUser();
  const manager = user?.role === "owner" || user?.role === "admin";
  const data = useModelsData(manager);
  const actions = useModelsActions({
    reload: data.load,
    setCatalogState: data.setCatalogState,
    setSelectedTarget: data.setSelectedTarget,
  });

  return {
    ...data,
    ...actions,
    manager,
    error: actions.error || data.error,
    busy: actions.busy || data.busy,
  };
}
