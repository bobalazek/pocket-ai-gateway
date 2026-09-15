import { describe, expect, it } from "vitest";

import { effectiveKeyState } from "@/features/keys/utils/key.utils";
import type { GatewayKey } from "@/features/keys/types/keys.types";

describe("effectiveKeyState", () => {
  it("reports expired active and disabled keys", () => {
    const key = { state: "active", expires_at: "2026-01-01T00:00:00Z" } as GatewayKey;
    expect(effectiveKeyState(key, Date.parse("2026-01-02T00:00:00Z"))).toBe("expired");
    expect(effectiveKeyState({ ...key, state: "disabled" }, Date.parse("2026-01-02T00:00:00Z"))).toBe("expired");
  });
});
