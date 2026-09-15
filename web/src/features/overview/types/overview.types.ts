import type { GatewayRequest } from "@/features/requests/types/requests.types";
import type { UsageSummary } from "@/features/usage/types/usage.types";

export type OverviewData = {
  usage: UsageSummary | null;
  recentRequests: GatewayRequest[];
  recentRequestsAvailable: boolean;
  diagnosticsVersion: string | null;
  setup: { providers: boolean; models: boolean; keys: boolean } | null;
  partial: boolean;
};
