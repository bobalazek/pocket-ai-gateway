"use client";

import { useEffect, useState } from "react";

import type { GatewayInferenceScope, GatewayUser } from "@/features/auth/types/auth.types";
import { isPublicAuthRoute, isSetupRoute, unauthenticatedDestination } from "@/lib/dashboard-routing";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useSetupGate() {
  const [state, setState] = useState<{ ready: boolean; user: GatewayUser | null; inferenceScopes: GatewayInferenceScope[]; error?: string }>({ ready: false, user: null, inferenceScopes: [] });

  useEffect(() => {
    const controller = new AbortController(); const currentPath = window.location.pathname; const isSetupPath = isSetupRoute(currentPath);
    void pocketAIGatewayAdmin.auth.setupStatus(controller.signal).then(async ({ setup_required: setupRequired }) => {
      if (setupRequired && !isSetupPath) { window.location.replace("/_/setup/"); return; }
      if (setupRequired) { setState({ ready: true, user: null, inferenceScopes: [] }); return; }
      try {
        const { user, inference_scopes: inferenceScopes } = await pocketAIGatewayAdmin.auth.session(controller.signal);
        if (isPublicAuthRoute(currentPath)) window.location.replace("/_/"); else setState({ ready: true, user, inferenceScopes });
      } catch (error) {
        if (error instanceof GatewayAPIError && error.status === 401) {
          const destination = unauthenticatedDestination(currentPath);
          if (destination) window.location.replace(destination); else setState({ ready: true, user: null, inferenceScopes: [] });
          return;
        }
        throw error;
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setState({ ready: false, user: null, inferenceScopes: [], error: error instanceof Error ? error.message : "Gateway unavailable" });
    });
    return () => controller.abort();
  }, []);

  return state;
}
