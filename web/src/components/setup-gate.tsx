"use client";

import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import { buttonVariants } from "@/components/ui/button";
import { GatewayAPIError, gatewayAPI, type GatewayUser } from "@/lib/api-client";
import { isPublicAuthRoute, isSetupRoute, unauthenticatedDestination } from "@/lib/dashboard-routing";

const SessionContext = createContext<GatewayUser | null>(null);

export function useGatewayUser() {
  return useContext(SessionContext);
}

export function SetupGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ ready: boolean; user: GatewayUser | null; error?: string }>({ ready: false, user: null });

  useEffect(() => {
    const controller = new AbortController();
    const setupPath = "/_/setup/";
    const currentPath = window.location.pathname;
    const isSetupPath = isSetupRoute(currentPath);
    void gatewayAPI.setupStatus(controller.signal).then(async ({ setup_required: setupRequired, setup_recovery_available: recoveryAvailable }) => {
      if (setupRequired && !isSetupPath) {
        window.location.replace(setupPath);
        return;
      }
      if (setupRequired) {
        setState({ ready: true, user: null });
        return;
      }
      try {
        const { user } = await gatewayAPI.session(controller.signal);
        if (isPublicAuthRoute(currentPath)) window.location.replace("/_/");
        else setState({ ready: true, user });
      } catch (error) {
        if (error instanceof GatewayAPIError && error.status === 401) {
		  const destination = unauthenticatedDestination(currentPath, recoveryAvailable);
		  if (destination) window.location.replace(destination);
		  else setState({ ready: true, user: null });
          return;
        }
        throw error;
      }
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setState({ ready: false, user: null, error: error instanceof Error ? error.message : "Gateway unavailable" });
    });
    return () => controller.abort();
  }, []);

  if (state.error) {
    return (
      <main id="main-content" className="gate-state">
        <p className="context">Connection problem</p>
        <h1>Dashboard unavailable</h1>
        <p className="lede">{state.error}</p>
        <button className={buttonVariants()} onClick={() => window.location.reload()}>Try again</button>
      </main>
    );
  }
  if (!state.ready) {
    return <main id="main-content" className="gate-state" aria-live="polite">Opening your gateway…</main>;
  }
  return <SessionContext.Provider value={state.user}>{children}</SessionContext.Provider>;
}
