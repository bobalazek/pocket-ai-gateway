import type { EffectiveLimit, LimitPolicy, OutboxStatus, PriceVersion, UnresolvedAttempt, UsageFilters, UsageSummary } from "@/features/usage/types/usage.types";
import { gatewayTransport } from "@/lib/api-client";

function queryFor(filters: UsageFilters, cursor = "") {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
  if (cursor) query.set("cursor", cursor);
  return query.size ? `?${query}` : "";
}

export const usageClient = {
  summary: (filters: UsageFilters = {}) => gatewayTransport.request<{ usage: UsageSummary }>(`/api/v1/usage${queryFor(filters)}`),
  unresolved: (filters: UsageFilters = {}, cursor = "") => gatewayTransport.request<{ data: UnresolvedAttempt[]; next_cursor: string; has_more: boolean }>(`/api/v1/usage/unresolved${queryFor(filters, cursor)}`),
  policies: (cursor = "") => gatewayTransport.request<{ data: LimitPolicy[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/policies${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`),
  effectiveLimits: (keyID: string, connectionID = "") => gatewayTransport.request<{ data: EffectiveLimit[] }>(`/api/v1/keys/${encodeURIComponent(keyID)}/effective-limits${connectionID ? `?connection_id=${encodeURIComponent(connectionID)}` : ""}`),
  createPolicy: (input: Omit<LimitPolicy, "id" | "revision" | "created_at" | "updated_at" | "limit_usd"> & { limit_usd?: string }) => gatewayTransport.request<{ policy: LimitPolicy }>("/api/v1/admin/policies", { method: "POST", body: input }),
  updatePolicy: (policy: LimitPolicy, input: { limit_units: number; limit_usd: string; enabled: boolean }) => gatewayTransport.request<{ policy: LimitPolicy }>(`/api/v1/admin/policies/${encodeURIComponent(policy.id)}`, { method: "PATCH", revision: policy.revision, body: input }),
  prices: (cursor = "") => gatewayTransport.request<{ data: PriceVersion[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/prices${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`),
  createPrice: (input: { connection_id: string; model_id: string; input_usd_per_million: string; output_usd_per_million: string; source: string; effective_from: string; effective_to: string }) => gatewayTransport.request<{ price: PriceVersion }>("/api/v1/admin/prices", { method: "POST", body: input }),
  outbox: () => gatewayTransport.request<{ outbox: OutboxStatus }>("/api/v1/admin/usage/outbox"),
  previewReprice: (input: { connection_id: string; model_id: string; from: string; to: string }) => gatewayTransport.request<{ preview: { affected_attempts: number; missing_prices: number; delta_usd: string } }>("/api/v1/admin/usage/reprice-preview", { method: "POST", body: input }),
  applyReprice: (input: { connection_id: string; model_id: string; from: string; to: string; idempotency_key: string }) => gatewayTransport.request<{ result: { affected_attempts: number; missing_prices: number; delta_usd: string } }>("/api/v1/admin/usage/reprice", { method: "POST", body: input }),
  adjust: (input: { attempt_id: string; delta_usd: string; reason: string; idempotency_key: string }) => gatewayTransport.request<{ restated_cost_usd: string }>("/api/v1/admin/usage/adjustments", { method: "POST", body: input }),
  reconcile: (input: { attempt_id: string; input_tokens: number; output_tokens: number; cost_usd?: string; usage_status: "provider_reported" | "estimated"; reason: string; idempotency_key: string }) => gatewayTransport.request<{ reconciled: boolean }>("/api/v1/admin/usage/reconciliations", { method: "POST", body: input }),
};
