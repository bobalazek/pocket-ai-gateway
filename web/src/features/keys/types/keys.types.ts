export type GatewayKey = {
  id: string;
  label: string;
  state: "active" | "disabled" | "revoked";
  scopes: string[];
  model_patterns: string[];
  connection_ids: string[];
  expires_at: string | null;
  revision: number;
  created_at: string;
  updated_at: string;
};

export type OneTimeSecret = { value: string; label: string; keyID: string };
