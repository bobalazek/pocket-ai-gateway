import type { GatewayUser } from "@/features/auth/types/auth.types";
import type { GatewaySession } from "@/features/auth/types/auth.types";
import { gatewayTransport } from "@/lib/api-client";

export const accountClient = {
  logout: () => gatewayTransport.request<void>("/api/v1/auth/logout", { method: "POST" }),
  sessions: () => gatewayTransport.request<{ items: GatewaySession[] }>("/api/v1/auth/sessions"),
  revokeSession: (id: string) => gatewayTransport.request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  changePassword: (input: { current_password: string; new_password: string }) => gatewayTransport.request<void>("/api/v1/auth/password", { method: "POST", body: input }),
  updateProfile: (input: { email: string; display_name: string; current_password: string }) => gatewayTransport.request<{ user: GatewayUser }>("/api/v1/me", { method: "PATCH", body: input }),
};
