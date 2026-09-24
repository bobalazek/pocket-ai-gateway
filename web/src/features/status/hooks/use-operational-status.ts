"use client";

import { useEffect, useState } from "react";

import type { OperationalStatus } from "@/features/status/types/status.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useOperationalStatus(enabled: boolean) {
  const [status, setStatus] = useState<OperationalStatus | null>(null);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(enabled);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    void pocketAIGatewayAdmin.status.operational(controller.signal)
      .then(({ status: result }) => { if (!controller.signal.aborted) { setStatus(result); setError(false); } })
      .catch(() => { if (!controller.signal.aborted) setError(true); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [enabled, revision]);

  return { status, error, loading, refresh: () => { setLoading(true); setRevision((value) => value + 1); } };
}
