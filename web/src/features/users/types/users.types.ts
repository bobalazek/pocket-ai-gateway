import type { GatewayGrants, GatewayUser } from "@/features/auth/types/auth.types";

export type ManagedUser = GatewayUser & {
  revision: number;
  created_at: string;
  updated_at: string;
  grants: GatewayGrants;
};

export type OneTimeCode = { value: string; user: string; userID: string; purpose: string };
