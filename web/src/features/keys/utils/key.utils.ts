import type { GatewayKey } from "@/features/keys/types/keys.types";

export function effectiveKeyState(key: GatewayKey, now = Date.now()) {
  return key.state !== "revoked" && key.expires_at && Date.parse(key.expires_at) <= now ? "expired" : key.state;
}
