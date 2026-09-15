export type GatewayGrants = {
  unrestricted: boolean;
  scopes: string[];
  model_patterns: string[];
  connection_ids: string[];
};

export type GatewayUser = {
  id: string;
  email: string;
  display_name: string;
  role: "owner" | "admin" | "member";
  status: "active" | "suspended" | "pending_activation" | "archived";
  grants: GatewayGrants;
};

export type GatewaySession = {
  id: string;
  current: boolean;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  authenticated_at: string;
  user_agent: string;
};

export const inferenceScopes = [
  "chat:generate",
  "responses:generate",
  "embeddings:generate",
  "moderations:classify",
  "images:generate",
  "images:edit",
  "images:variation",
  "audio:speech",
  "audio:transcribe",
  "audio:translate",
  "models:read",
  "tokens:count",
] as const;

export type InferenceScope = (typeof inferenceScopes)[number];
