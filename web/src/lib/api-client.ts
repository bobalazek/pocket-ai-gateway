export type GatewayErrorBody = {
  error?: {
    code?: string;
    message?: string;
    policy_id?: string;
    metric?: string;
  };
};

export class GatewayAPIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
    readonly requestID: string | null,
    readonly policyID: string | null = null,
    readonly metric: string | null = null,
    readonly retryAfter: number | null = null,
  ) {
    super(message);
    this.name = "GatewayAPIError";
  }
}

export type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  csrfToken?: string;
  authorization?: string;
  anthropicKey?: string;
  geminiKey?: string;
  revision?: number;
  signal?: AbortSignal;
  redirectOnUnauthorized?: boolean;
};

const allowedPaths = ["/api/v1/", "/api/openai/", "/api/anthropic/", "/api/gemini/"];

export class GatewayAPIClient {
  constructor(private readonly fetcher: typeof fetch = (input, init) => globalThis.fetch(input, init)) {}

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    assertSameOriginGatewayPath(path);
    const method = options.method ?? "GET";
    const headers = requestHeaders(method, options);
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

  async stream(path: string, options: RequestOptions, onChunk: (chunk: string) => void): Promise<void> {
    assertSameOriginGatewayPath(path);
    const method = options.method ?? "GET";
    const headers = requestHeaders(method, options);
    headers.set("Accept", "text/event-stream");
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
    if (!response.body) return;
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      onChunk(decoder.decode(value, { stream: true }));
    }
    onChunk(decoder.decode());
  }
}

export const gatewayTransport = new GatewayAPIClient();

function requestHeaders(method: string, options: RequestOptions) {
  const headers = new Headers({ Accept: "application/json" });
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  const csrfToken = options.csrfToken ?? (method === "GET" ? "" : readCookie("pocket_ai_gateway_csrf"));
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);
  if (options.authorization) headers.set("Authorization", options.authorization);
  if (options.anthropicKey) {
    headers.set("x-api-key", options.anthropicKey);
    headers.set("anthropic-version", "2023-06-01");
  }
  if (options.geminiKey) headers.set("x-goog-api-key", options.geminiKey);
  if (options.revision) headers.set("If-Match", `"${options.revision}"`);
  return headers;
}

function readCookie(name: string) {
  if (typeof document === "undefined") return "";
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split("; ").find((cookie) => cookie.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : "";
}

function assertSameOriginGatewayPath(path: string) {
  const parsed = new URL(path, "http://gateway.invalid");
  const allowed = parsed.pathname === "/healthz" || parsed.pathname === "/readyz" || allowedPaths.some((prefix) => parsed.pathname.startsWith(prefix));
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
    body.error?.policy_id ?? null,
    body.error?.metric ?? null,
    response.headers.has("Retry-After") ? Number(response.headers.get("Retry-After")) : null,
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
    && (!("message" in error) || typeof error.message === "string")
    && (!("policy_id" in error) || typeof error.policy_id === "string")
    && (!("metric" in error) || typeof error.metric === "string");
}
