"use client";

import { useGatewayUser } from "@/components/setup-gate";
import { useUsageActions } from "@/features/usage/hooks/use-usage-actions";
import { useUsageData } from "@/features/usage/hooks/use-usage-data";

export function useUsage() {
  const user = useGatewayUser();
  const isOwner = user?.role === "owner";
  const canManage = isOwner || user?.role === "admin";
  const data = useUsageData(canManage);
  const actions = useUsageActions(data.load);

  return {
    ...data,
    ...actions,
    isOwner,
    canManage,
    error: actions.error || data.error,
    busy: actions.busy || data.busy,
  };
}
