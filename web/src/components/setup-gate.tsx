"use client";

import { createContext, useContext, type ReactNode } from "react";

import { buttonVariants } from "@/components/ui/button";
import { useSetupGate } from "@/features/auth/hooks/use-setup-gate";
import type { GatewayInferenceScope, GatewayUser } from "@/features/auth/types/auth.types";

const SessionContext = createContext<{ user: GatewayUser | null; inferenceScopes: GatewayInferenceScope[] }>({ user: null, inferenceScopes: [] });

export function useGatewayUser() {
  return useContext(SessionContext).user;
}

export function useGatewayInferenceScopes() {
  return useContext(SessionContext).inferenceScopes;
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
  return <SessionContext.Provider value={{ user: state.user, inferenceScopes: state.inferenceScopes }}>{children}</SessionContext.Provider>;
}
