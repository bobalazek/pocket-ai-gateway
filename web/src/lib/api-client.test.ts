import { describe, expect, it, vi } from "vitest";

import { effectiveKeyState, GatewayAPIClient, GatewayAPIError, type GatewayKey } from "./api-client";

describe("GatewayAPIClient", () => {
  it("reports an expired active key by its effective state", () => {
    const key = { state: "active", expires_at: "2026-01-01T00:00:00Z" } as GatewayKey;
    expect(effectiveKeyState(key, Date.parse("2026-01-02T00:00:00Z"))).toBe("expired");
		expect(effectiveKeyState({ ...key, state: "disabled" }, Date.parse("2026-01-02T00:00:00Z"))).toBe("expired");
  });

  it("uses the same-origin request policy for health", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: "ok" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(new GatewayAPIClient(fetcher).health()).resolves.toEqual({ status: "ok" });
    expect(fetcher).toHaveBeenCalledWith("/healthz", expect.objectContaining({
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
    }));
  });

  it("serializes mutations and applies transient credentials", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));
    const client = new GatewayAPIClient(fetcher);

    await client.request<void>("/api/v1/users/", {
      method: "POST",
      body: { email: "owner@example.test" },
      csrfToken: "csrf-test",
      authorization: "Bearer ephemeral-test",
    });

    const [, init] = fetcher.mock.calls[0];
    const headers = init?.headers as Headers;
    expect(init?.body).toBe('{"email":"owner@example.test"}');
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-test");
    expect(headers.get("Authorization")).toBe("Bearer ephemeral-test");
  });

  it.each([
    "https://example.com/api/v1/users/",
    "//example.com/api/v1/users/",
    "/api/v1/%2e%2e/users/",
    "/unscoped",
  ])("rejects an unsafe path before dispatch: %s", async (path) => {
    const fetcher = vi.fn<typeof fetch>();
    await expect(new GatewayAPIClient(fetcher).request(path)).rejects.toBeInstanceOf(TypeError);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("returns a typed safe API error", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "denied", message: "Access denied" } }), {
        status: 403,
        headers: { "X-Request-ID": "request-test" },
      }),
    );

    await expect(new GatewayAPIClient(fetcher).request("/api/v1/me/"))
      .rejects.toEqual(new GatewayAPIError("Access denied", 403, "denied", "request-test"));
  });

  it.each([null, { error: "wrong shape" }, ["unexpected"]])("normalizes malformed JSON errors: %j", async (body) => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify(body), { status: 502, headers: { "X-Request-ID": "request-test" } }),
    );

    await expect(new GatewayAPIClient(fetcher).request("/api/v1/me/"))
      .rejects.toEqual(new GatewayAPIError("Gateway request failed with status 502", 502, "request_failed", "request-test"));
  });
});
