"use client";

import { useEffect, useState } from "react";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export type RuntimeHealth = "checking" | "ready" | "unavailable";

export function useRuntimeHealth() {
  const [liveness, setLiveness] = useState<RuntimeHealth>("checking");
  const [readiness, setReadiness] = useState<RuntimeHealth>("checking");

  useEffect(() => {
    const controller = new AbortController();
    void pocketAIGatewayAdmin.status.health(controller.signal)
      .then(({ status }) => { if (!controller.signal.aborted) setLiveness(status === "ok" ? "ready" : "unavailable"); })
      .catch(() => { if (!controller.signal.aborted) setLiveness("unavailable"); });
    void pocketAIGatewayAdmin.status.readiness(controller.signal)
      .then(({ ready }) => { if (!controller.signal.aborted) setReadiness(ready ? "ready" : "unavailable"); })
      .catch(() => { if (!controller.signal.aborted) setReadiness("unavailable"); });
    return () => controller.abort();
  }, []);

  return { liveness, readiness };
}
