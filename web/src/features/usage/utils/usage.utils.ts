import type { UsageFilters, UsageSummary } from "@/features/usage/types/usage.types";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export const emptyUsage: UsageSummary = { from: "", to: "", requests: 0, attempts: 0, input_tokens: 0, output_tokens: 0, known_cost_usd: "0", estimated_cost_usd: "0", as_recorded_cost_usd: "0", restatement_delta_usd: "0", unknown_attempts: 0, points: [] };
export const datetime = (value: FormDataEntryValue | null) => value ? new Date(String(value)).toISOString() : "";
export const localDatetime = (value?: string) => {
  if (!value) return ""; const date = new Date(value); if (Number.isNaN(date.getTime())) return ""; const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
};
export const failureText = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? `${error.message}${error.metric ? ` (${error.metric})` : ""}${error.retryAfter ? ` Retry in ${error.retryAfter}s.` : ""}` : fallback;
const isRFC3339 = (value: string) => /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && !Number.isNaN(new Date(value).getTime());
export const readUsageFilters = (): UsageFilters => {
  if (typeof window === "undefined") return {};
  const query = new URLSearchParams(window.location.search); const filters: UsageFilters = {};
  for (const key of ["from", "to", "user_id", "key_id", "model_id", "connection_id"] as const) { const value = query.get(key); if (value && (!(key === "from" || key === "to") || isRFC3339(value))) filters[key] = value; }
  return filters;
};
export const policyKinds = {
  quota_requests: ["requests", "quota"], quota_tokens: ["tokens", "quota"], quota_spend: ["spend", "quota"],
  bucket_requests: ["requests", "token_bucket"], bucket_tokens: ["tokens", "token_bucket"], window_requests: ["requests", "fixed_window"], window_tokens: ["tokens", "fixed_window"],
  concurrency: ["concurrency", "concurrency"], body: ["body_bytes", "ceiling"], output: ["output_tokens", "ceiling"], batch: ["batch_items", "ceiling"],
} as const;
