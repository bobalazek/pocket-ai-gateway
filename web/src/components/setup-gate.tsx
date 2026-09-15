"use client";

import { createContext, useContext, type ReactNode } from "react";

import { buttonVariants } from "@/components/ui/button";
import { useSetupGate } from "@/features/auth/hooks/use-setup-gate";
import type { GatewayUser } from "@/features/auth/types/auth.types";

const SessionContext = createContext<GatewayUser | null>(null);

export function useGatewayUser() {
  return useContext(SessionContext);
}

export function SetupGate({ children }: { children: ReactNode }) {
  const state = useSetupGate();

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
