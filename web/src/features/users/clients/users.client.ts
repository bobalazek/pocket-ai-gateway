import type { GatewayGrants } from "@/features/auth/types/auth.types";
import type { ManagedUser } from "@/features/users/types/users.types";
import { gatewayTransport } from "@/lib/api-client";

export const usersClient = {
  list: (cursor = "") => gatewayTransport.request<{ data: ManagedUser[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/users${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`),
  create: (input: { email: string; display_name: string; role: "admin" | "member"; grants: GatewayGrants }) => gatewayTransport.request<{ user: ManagedUser; activation_code: string }>("/api/v1/admin/users", { method: "POST", body: input }),
  update: (id: string, revision: number, input: { display_name?: string; email?: string; status?: "active" | "suspended" }) => gatewayTransport.request<{ user: ManagedUser }>(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: "PATCH", body: input, revision }),
  issueCode: (id: string, purpose: "activation" | "recovery") => gatewayTransport.request<{ code: string }>(`/api/v1/admin/users/${encodeURIComponent(id)}/${purpose}-code`, { method: "POST" }),
  updateGrants: (id: string, revision: number, grants: GatewayGrants) => gatewayTransport.request<{ user: ManagedUser }>(`/api/v1/admin/users/${encodeURIComponent(id)}/grants`, { method: "PUT", body: grants, revision }),
  transferOwner: (userID: string, revision: number) => gatewayTransport.request<void>("/api/v1/admin/owner/transfer", { method: "POST", body: { user_id: userID, revision } }),
};
