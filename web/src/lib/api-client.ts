export type GatewayErrorBody = {
  error?: {
    code?: string;
    message?: string;
  };
};

export type GatewayUser = {
  id: string;
  email: string;
  display_name: string;
  role: "owner" | "admin" | "member";
  status: "active" | "suspended" | "pending_activation" | "archived";
	grants: GatewayGrants;
};

export type GatewayGrants = { unrestricted: boolean; scopes: string[]; model_patterns: string[]; connection_ids: string[] };
export type ManagedUser = GatewayUser & { revision: number; created_at: string; updated_at: string; grants: GatewayGrants };
export type GatewaySession = { id: string; current: boolean; created_at: string; last_seen_at: string; expires_at: string; authenticated_at: string; user_agent: string };
export type GatewayKey = { id: string; label: string; state: "active" | "disabled" | "revoked"; scopes: string[]; model_patterns: string[]; connection_ids: string[]; expires_at: string | null; revision: number; created_at: string; updated_at: string };

export function effectiveKeyState(key: GatewayKey, now = Date.now()) {
  return key.state !== "revoked" && key.expires_at && Date.parse(key.expires_at) <= now ? "expired" : key.state;
}

export class GatewayAPIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
    readonly requestID: string | null,
  ) {
    super(message);
    this.name = "GatewayAPIError";
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  csrfToken?: string;
  authorization?: string;
  revision?: number;
  signal?: AbortSignal;
	redirectOnUnauthorized?: boolean;
};

const allowedPaths = ["/api/v1/", "/api/openai/", "/api/anthropic/", "/api/gemini/"];

export class GatewayAPIClient {
  constructor(private readonly fetcher: typeof fetch = (input, init) => globalThis.fetch(input, init)) {}

  health(signal?: AbortSignal) {
    return this.request<{ status: "ok" }>("/healthz", { signal });
  }

  setupStatus(signal?: AbortSignal) {
    return this.request<{ setup_required: boolean; setup_recovery_available: boolean }>("/api/v1/auth/setup/status", { signal });
  }

  claimOwner(input: { setup_code: string; email: string; display_name: string; password: string }, signal?: AbortSignal) {
    return this.request<{ user: GatewayUser }>("/api/v1/auth/setup/claim", { method: "POST", body: input, signal });
  }

  session(signal?: AbortSignal) {
		return this.request<{ user: GatewayUser; session: GatewaySession }>("/api/v1/auth/session", { signal, redirectOnUnauthorized: false });
  }

  login(input: { email: string; password: string }) {
    return this.request<{ user: GatewayUser }>("/api/v1/auth/login", { method: "POST", body: input });
  }

  activate(input: { code: string; password: string }) {
    return this.request<{ user: GatewayUser }>("/api/v1/auth/activate", { method: "POST", body: input });
  }

  logout() { return this.request<void>("/api/v1/auth/logout", { method: "POST" }); }
  sessions() { return this.request<{ items: GatewaySession[] }>("/api/v1/auth/sessions"); }
  revokeSession(id: string) { return this.request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }); }
  changePassword(input: { current_password: string; new_password: string }) { return this.request<void>("/api/v1/auth/password", { method: "POST", body: input }); }
  updateProfile(input: { email: string; display_name: string; current_password: string }) { return this.request<{ user: GatewayUser }>("/api/v1/me", { method: "PATCH", body: input }); }

  users(cursor = "") { return this.request<{ data: ManagedUser[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/users${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`); }
  createUser(input: { email: string; display_name: string; role: "admin" | "member"; grants: GatewayGrants }) { return this.request<{ user: ManagedUser; activation_code: string }>("/api/v1/admin/users", { method: "POST", body: input }); }
  updateUser(id: string, revision: number, input: { display_name?: string; email?: string; status?: "active" | "suspended" }) { return this.request<{ user: ManagedUser }>(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: "PATCH", body: input, revision }); }
  userCode(id: string, purpose: "activation" | "recovery") { return this.request<{ code: string }>(`/api/v1/admin/users/${encodeURIComponent(id)}/${purpose}-code`, { method: "POST" }); }
  updateUserGrants(id: string, revision: number, grants: GatewayGrants) { return this.request<{ user: ManagedUser }>(`/api/v1/admin/users/${encodeURIComponent(id)}/grants`, { method: "PUT", body: grants, revision }); }
  transferOwner(userID: string, revision: number) { return this.request<void>("/api/v1/admin/owner/transfer", { method: "POST", body: { user_id: userID, revision } }); }

  keys(cursor = "") { return this.request<{ data: GatewayKey[]; next_cursor: string; has_more: boolean }>(`/api/v1/keys${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`); }
  createKey(input: { label: string; scopes: string[]; model_patterns: string[]; connection_ids: string[]; expires_at: string }) { return this.request<{ key: GatewayKey; secret: string }>("/api/v1/keys", { method: "POST", body: input }); }
  updateKey(id: string, revision: number, input: { label: string; state: "active" | "disabled"; scopes: string[]; model_patterns: string[]; connection_ids: string[]; expires_at: string }) { return this.request<{ key: GatewayKey }>(`/api/v1/keys/${encodeURIComponent(id)}`, { method: "PATCH", body: input, revision }); }
  rotateKey(id: string, revision: number) { return this.request<{ key: GatewayKey; secret: string }>(`/api/v1/keys/${encodeURIComponent(id)}/rotate`, { method: "POST", revision }); }
  revokeKey(id: string, revision: number) { return this.request<void>(`/api/v1/keys/${encodeURIComponent(id)}`, { method: "DELETE", revision }); }

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    assertSameOriginGatewayPath(path);
    const method = options.method ?? "GET";
    const headers = new Headers({ Accept: "application/json" });
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    const csrfToken = options.csrfToken ?? (method === "GET" ? "" : readCookie("pocket_ai_gateway_csrf"));
    if (csrfToken) headers.set("X-CSRF-Token", csrfToken);
    if (options.authorization) headers.set("Authorization", options.authorization);
    if (options.revision) headers.set("If-Match", `"${options.revision}"`);

    const response = await this.fetcher(path, {
      method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
      signal: options.signal,
    });
		if (!response.ok) throw await gatewayError(response, path, options.redirectOnUnauthorized !== false);
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }
}

function readCookie(name: string) {
  if (typeof document === "undefined") return "";
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split("; ").find((cookie) => cookie.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : "";
}

function assertSameOriginGatewayPath(path: string) {
  const parsed = new URL(path, "http://gateway.invalid");
  const allowed = parsed.pathname === "/healthz" || allowedPaths.some((prefix) => parsed.pathname.startsWith(prefix));
  if (parsed.origin !== "http://gateway.invalid" || !path.startsWith("/") || !allowed || path.includes("\\")) {
    throw new TypeError("Gateway API paths must be same-origin and use a documented namespace");
  }
}

async function gatewayError(response: Response, path: string, redirectOnUnauthorized: boolean) {
  let body: GatewayErrorBody = {};
  try {
    const parsed: unknown = await response.json();
    if (isGatewayErrorBody(parsed)) body = parsed;
  } catch {
    // Upstream and proxy errors are allowed to be non-JSON; expose no response body.
  }
	const error = new GatewayAPIError(
    body.error?.message ?? `Gateway request failed with status ${response.status}`,
    response.status,
    body.error?.code ?? "request_failed",
    response.headers.get("X-Request-ID"),
  );
	if (redirectOnUnauthorized && response.status === 401 && typeof window !== "undefined" && !["/api/v1/auth/login", "/api/v1/auth/activate", "/api/v1/auth/setup/claim"].includes(path)) {
		window.location.replace("/_/login/?reason=session-expired");
	}
	return error;
}

function isGatewayErrorBody(value: unknown): value is GatewayErrorBody {
  if (typeof value !== "object" || value === null || !("error" in value)) return false;
  const error = value.error;
  if (typeof error !== "object" || error === null) return false;
  return (!("code" in error) || typeof error.code === "string")
    && (!("message" in error) || typeof error.message === "string");
}

export const gatewayAPI = new GatewayAPIClient();
