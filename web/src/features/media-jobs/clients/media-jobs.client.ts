import type { MediaJob } from "@/features/media-jobs/types/media-jobs.types";
import { gatewayTransport } from "@/lib/api-client";

export const mediaJobsClient = {
  list: (limit = 50, signal?: AbortSignal) => gatewayTransport.request<{ data: MediaJob[] }>(`/api/v1/media/jobs?limit=${limit}`, { signal }),
  get: (id: string, signal?: AbortSignal) => gatewayTransport.request<{ job: MediaJob }>(`/api/v1/media/jobs/${encodeURIComponent(id)}`, { signal }),
  cancel: (id: string) => gatewayTransport.request<{ job: MediaJob }>(`/api/v1/media/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST" }),
};
