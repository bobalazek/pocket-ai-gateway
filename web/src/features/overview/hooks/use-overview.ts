"use client";

import { useEffect, useState } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { OverviewData } from "@/features/overview/types/overview.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

const empty: OverviewData = { usage: null, recentRequests: [], recentRequestsAvailable: false, diagnosticsVersion: null, setup: null, partial: false };

export function useOverview() {
  const user = useGatewayUser();
  const manager = user?.role === "owner" || user?.role === "admin";
  const [data, setData] = useState(empty);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let mounted = true;
    void (async () => {
      const [usage, recent] = await Promise.allSettled([pocketAIGatewayAdmin.usage.summary(), pocketAIGatewayAdmin.requests.list()]);
      const setupResults = manager ? await Promise.allSettled([pocketAIGatewayAdmin.providers.connections(), pocketAIGatewayAdmin.models.publicModels(), pocketAIGatewayAdmin.keys.list()]) : null;
      const diagnostics = user?.role === "owner" ? await Promise.allSettled([pocketAIGatewayAdmin.settings.diagnostics()]).then(([result]) => result) : null;
      if (!mounted) return;
      setData({
        usage: usage.status === "fulfilled" ? usage.value.usage : null,
        recentRequests: recent.status === "fulfilled" ? recent.value.data.slice(0, 5) : [],
        recentRequestsAvailable: recent.status === "fulfilled",
        setup: setupResults?.every((result) => result.status === "fulfilled") ? {
          providers: setupResults[0].status === "fulfilled" && setupResults[0].value.data.length > 0,
          models: setupResults[1].status === "fulfilled" && setupResults[1].value.data.length > 0,
          keys: setupResults[2].status === "fulfilled" && setupResults[2].value.data.length > 0,
        } : null,
        diagnosticsVersion: diagnostics?.status === "fulfilled" ? diagnostics.value.diagnostics.version : null,
        partial: usage.status === "rejected" || recent.status === "rejected" || Boolean(setupResults?.some((result) => result.status === "rejected")) || diagnostics?.status === "rejected",
      });
      setLoading(false);
    })();
    return () => { mounted = false; };
  }, [manager, user?.role]);

  return { data, loading };
}
