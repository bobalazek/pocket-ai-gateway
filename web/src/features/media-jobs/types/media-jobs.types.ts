export type MediaJob = {
  id: string;
  object: "media.job";
  state: "queued" | "submitting" | "starting" | "processing" | "canceling" | "succeeded" | "failed" | "canceled" | "interrupted_unknown";
  model: string;
  media_type: "image" | "video" | "audio" | "other";
  provider: string;
  provider_job_id?: string;
  error?: { code?: string; message?: string };
  cancel_requested: boolean;
  cancelable: boolean;
  created_at: string;
  updated_at: string;
  completed_at?: string;
};
