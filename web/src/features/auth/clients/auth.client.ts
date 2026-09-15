import { gatewayTransport } from "@/lib/api-client";
import type { GatewayUser } from "@/features/auth/types/auth.types";
import type { GatewaySession } from "@/features/auth/types/auth.types";

export const authClient = {
  setupStatus: (signal?: AbortSignal) => gatewayTransport.request<{ setup_required: boolean }>("/api/v1/auth/setup/status", { signal }),
  claimOwner: (input: { email: string; display_name: string; password: string }, signal?: AbortSignal) => gatewayTransport.request<{ user: GatewayUser }>("/api/v1/auth/setup/claim", { method: "POST", body: input, signal }),
  session: (signal?: AbortSignal) => gatewayTransport.request<{ user: GatewayUser; session: GatewaySession }>("/api/v1/auth/session", { signal, redirectOnUnauthorized: false }),
  login: (input: { email: string; password: string }) => gatewayTransport.request<{ user: GatewayUser }>("/api/v1/auth/login", { method: "POST", body: input }),
  activate: (input: { code: string; password: string }) => gatewayTransport.request<{ user: GatewayUser }>("/api/v1/auth/activate", { method: "POST", body: input }),
};
