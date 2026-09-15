"use client";

import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { useRuntimeHealth } from "@/features/status/hooks/use-runtime-health";

const labels = { checking: "Checking…", ready: "Ready", unavailable: "Unavailable" } as const;

export function RuntimeHealth({ compact = false }: { compact?: boolean }) {
  const { liveness, readiness } = useRuntimeHealth();

  if (compact) {
    return <><div aria-live="polite"><span>HTTP process</span><strong data-state={liveness}>{labels[liveness]}</strong></div><div aria-live="polite"><span>Operational readiness</span><strong data-state={readiness}>{labels[readiness]}</strong></div></>;
  }

  return (
    <Card className="readiness" role="region" aria-labelledby="readiness-title">
      <CardHeader className="section-heading">
        <h2 id="readiness-title">Runtime readiness</h2>
        <span className="status" data-state={readiness}><span aria-hidden="true" /> {labels[readiness]}</span>
      </CardHeader>
      <CardContent>
        <p>{readiness === "checking" ? "Checking whether the gateway can accept work…" : readiness === "ready" ? "The gateway reports that it can accept work." : "The process is reachable, but the gateway is not ready to accept work."}</p>
      </CardContent>
    </Card>
  );
}
