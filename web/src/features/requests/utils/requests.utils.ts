import type { RequestFilters } from "@/features/requests/types/requests.types";

export const requestFilterKeys = ["request_id", "user_id", "key_id", "model_id", "connection_id", "dialect", "operation", "state", "mode", "from", "to", "cursor"] as const;

export function readRequestFilters(search: string): RequestFilters {
  const query = new URLSearchParams(search);
  const filters: RequestFilters = {};
  for (const key of requestFilterKeys) {
    const value = query.get(key);
    if (value) filters[key] = value;
  }
  return filters;
}

export function requestSearch(filters: RequestFilters) {
  const query = new URLSearchParams();
  for (const key of requestFilterKeys) {
    const value = filters[key];
    if (value) query.set(key, value);
  }
  return query.toString();
}

export function requestListFilters(filters: RequestFilters): RequestFilters {
  const { request_id: _requestID, cursor: _cursor, ...listFilters } = filters;
  return listFilters;
}

/** Formats a millisecond duration for request history; unknown timings stay explicit. */
export function formatDuration(milliseconds: number | null | undefined) {
  if (milliseconds == null) return "—";
  return milliseconds < 1000 ? `${milliseconds} ms` : `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 2 : 1)} s`;
}
