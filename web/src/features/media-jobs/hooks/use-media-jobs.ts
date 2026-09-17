"use client";

import { useCallback, useEffect, useState } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { MediaJob } from "@/features/media-jobs/types/media-jobs.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useMediaJobs() {
  const user = useGatewayUser();
  const [items, setItems] = useState<MediaJob[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const result = await pocketAIGatewayAdmin.mediaJobs.list(50, signal);
      setItems(result.data);
      setError("");
    } catch (failure) {
      if (signal?.aborted) return;
      setError(failure instanceof GatewayAPIError ? failure.message : "Media jobs are unavailable");
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  async function cancel(item: MediaJob) {
    if (!window.confirm(`Cancel media job ${item.id}?`)) return;
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.mediaJobs.cancel(item.id);
      await load();
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Media job could not be canceled");
    } finally {
      setBusy(false);
    }
  }

  return { user, items, error, busy, load, cancel };
}
