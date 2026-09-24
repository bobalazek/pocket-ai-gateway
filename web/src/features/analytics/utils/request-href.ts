import type { UsageBreakdownDimension, UsageFilters, UsageSummary } from "@/features/usage/types/usage.types";

const fields: Record<UsageBreakdownDimension, string> = {
  key: "key_id", user: "user_id", model: "model_id", connection: "connection_id", dialect: "dialect", operation: "operation", state: "state",
};

export function analyticsRequestHref(filters: UsageFilters, usage: Pick<UsageSummary, "from" | "to">, dimension?: UsageBreakdownDimension, id?: string) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries({ ...filters, from: usage.from, to: usage.to })) if (value) query.set(key, value);
  if (dimension && id) query.set(fields[dimension], id);
  return `/requests/?${query}`;
}
