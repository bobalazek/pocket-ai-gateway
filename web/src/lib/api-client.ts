export type GatewayErrorBody = {
  error?: {
    code?: string;
    message?: string;
		policy_id?: string;
		metric?: string;
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
export type LimitPolicy = { id: string; scope_kind: "instance" | "user" | "key" | "connection"; scope_id: string; metric: "requests" | "tokens" | "spend" | "concurrency" | "body_bytes" | "output_tokens" | "batch_items"; algorithm: "token_bucket" | "fixed_window" | "quota" | "concurrency" | "ceiling"; period: "" | "hour" | "day" | "week" | "month" | "lifetime"; window_seconds: number; limit_units: number; limit_usd?: string; refill_units: number; refill_interval_ms: number; enabled: boolean; revision: number; created_at: string; updated_at: string };
export type UsagePoint = { date: string; requests: number; input_tokens: number; output_tokens: number; known_cost_usd: string; unknown_attempts: number };
export type UsageSummary = { from: string; to: string; requests: number; attempts: number; input_tokens: number; output_tokens: number; known_cost_usd: string; estimated_cost_usd: string; as_recorded_cost_usd: string; restatement_delta_usd: string; unknown_attempts: number; points: UsagePoint[] };
export type UnresolvedAttempt = { id: string; request_id: string; owner_user_id: string; key_id: string; model_id: string; connection_id: string; state: string; usage_status: string; estimated_cost_usd?: string; started_at: string };
export type PriceVersion = { id: string; connection_id: string; model_id: string; input_usd_per_million: string; output_usd_per_million: string; source: string; effective_from: string; effective_to: string | null; created_at: string };
export type OutboxStatus = { pending_events: number; reserved_events: number; pending_bytes: number; oldest_event_at: string | null; full: boolean };
export type EffectiveLimit = { policy_id: string; scope_kind: string; metric: string; algorithm: string; period: string; limit_units: number; limit_usd?: string; consumed_units: number; consumed_usd?: string; reserved_units: number; reserved_usd?: string; remaining_units: number; remaining_usd?: string; resets_at: string | null };
export type UsageFilters = { from?: string; to?: string; user_id?: string; key_id?: string; model_id?: string; connection_id?: string };
export type ProviderConnection = { id: string; name: string; adapter: "openai" | "anthropic" | "gemini" | "openai_compatible"; base_url: string; enabled: boolean; allow_private_network: boolean; timeout_ms: number; preset: string; credential_state: "missing" | "stored" | "external"; revision: number; created_at: string; updated_at: string };
export type ProviderPreset = { id: string; label: string; adapter: ProviderConnection["adapter"]; base_url?: string; base_url_required: boolean; credential_required: boolean; private_network: boolean; operations: string[]; documentation_url: string; reviewed_at: string };
export type UpstreamModel = { id: string; connection_id: string; upstream_id: string; capabilities: string[]; active: boolean };
export type RouteStrategy = "fixed" | "ordered_fallback" | "weighted" | "lowest_cost" | "lowest_latency";
export type PublicModel = { id: string; label: string; description: string; target_connection_id: string; target_model_id: string; upstream_id: string; adapter: string; capabilities: string[]; active: boolean; revision: number; routing_strategy: RouteStrategy; free_only: boolean };
export type RouteTargetInput = { upstream_model_id: string; priority: number; weight: number; enabled: boolean };
export type RoutePlan = { model_id: string; strategy: RouteStrategy; free_only: boolean; selection_reason: string; targets: { upstream_model_id: string; connection_id: string; upstream_id: string; adapter: string; capabilities: string[]; priority: number; weight: number; estimated_cost_usd?: string; latency_ms?: number; sample_count: number; circuit_open_until?: string }[]; rejected: { upstream_model_id: string; connection_id: string; reason: string }[] };
export type CatalogCandidate = { provider: string; model_id: string; label: string; capabilities: string[]; input_nanos_per_million?: number; output_nanos_per_million?: number; free: boolean; source: string; source_version: string; discovered_at: string };
export type RoutePreviewInput = { operation: string; streaming: boolean; estimated_input_tokens: number; estimated_output_tokens: number };
export type CatalogState = { source_url: string; source_version: string; last_checked_at: string | null; last_error: string; refresh_enabled: boolean; refresh_interval_hours: number };
export type CatalogModel = Pick<PublicModel, "id" | "label" | "description" | "adapter" | "capabilities">;
export type GatewayRequest = { id: string; owner_user_id: string; key_id: string; operation: string; dialect: string; model_id: string; state: string; started_at: string; finished_at: string | null; attempts: { id: string; ordinal: number; connection_id: string; model_id: string; upstream_model_id: string; target_dialect: string; target_operation: string; translation_applied: boolean; request_tool_count: number; response_tool_call_count: number; tool_call_status: string; selection_reason: string; rejected_candidates: { upstream_model_id: string; connection_id: string; reason: string }[]; state: string; usage_status: string; input_tokens: number; output_tokens: number; cost_usd: string | null; started_at: string }[] };
export type OperationSettings = { backup_enabled: boolean; backup_interval_hours: number; backup_retention_count: number; backup_destination: "local" | "s3"; local_directory: string; s3_endpoint: string; s3_region: string; s3_bucket: string; s3_prefix: string; s3_access_key_env: string; s3_secret_key_env: string; request_retention_days: number; audit_retention_days: number; revision: number; updated_at: string; backup_key_configured: boolean };
export type BackupJob = { id: string; state: "running" | "succeeded" | "failed"; destination: "local" | "s3"; archive_name: string; checksum: string; size_bytes: number; snapshot_generation: string; error: string; started_at: string; finished_at?: string };
export type AuditEvent = { id: string; actor_user_id?: string; action: string; resource_type: string; resource_id: string; detail_json: string; created_at: string };
export type AuditFilters = { actor_user_id?: string; action?: string; resource_type?: string; from?: string; to?: string; cursor?: string };
export type Diagnostics = { version: string; sqlite_version: string; uptime_seconds: number; system_database_bytes: number; data_database_bytes: number; pending_outbox_events: number; backup_key_configured: boolean; latest_backup_state: string };
export type ConfigPreview = { connections: number; upstream_models: number; public_models: number; targets: number; policies: number; prices: number; warnings: string[] };

export function effectiveKeyState(key: GatewayKey, now = Date.now()) {
  return key.state !== "revoked" && key.expires_at && Date.parse(key.expires_at) <= now ? "expired" : key.state;
}

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

type RequestOptions = {
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

  health(signal?: AbortSignal) {
    return this.request<{ status: "ok" }>("/healthz", { signal });
  }

  setupStatus(signal?: AbortSignal) {
    return this.request<{ setup_required: boolean }>("/api/v1/auth/setup/status", { signal });
  }

  claimOwner(input: { email: string; display_name: string; password: string }, signal?: AbortSignal) {
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

  usage(filters: UsageFilters = {}) { const query = new URLSearchParams(); for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value); return this.request<{ usage: UsageSummary }>(`/api/v1/usage${query.size ? `?${query}` : ""}`); }
  unresolvedUsage(filters: UsageFilters = {}, cursor = "") { const query = new URLSearchParams(); for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value); if (cursor) query.set("cursor", cursor); return this.request<{ data: UnresolvedAttempt[]; next_cursor: string; has_more: boolean }>(`/api/v1/usage/unresolved${query.size ? `?${query}` : ""}`); }
  policies(cursor = "") { return this.request<{ data: LimitPolicy[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/policies${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`); }
	effectiveLimits(keyID: string, connectionID = "") { const query = connectionID ? `?connection_id=${encodeURIComponent(connectionID)}` : ""; return this.request<{ data: EffectiveLimit[] }>(`/api/v1/keys/${encodeURIComponent(keyID)}/effective-limits${query}`); }
  createPolicy(input: Omit<LimitPolicy, "id" | "revision" | "created_at" | "updated_at" | "limit_usd"> & { limit_usd?: string }) { return this.request<{ policy: LimitPolicy }>("/api/v1/admin/policies", { method: "POST", body: input }); }
  updatePolicy(policy: LimitPolicy, input: { limit_units: number; limit_usd: string; enabled: boolean }) { return this.request<{ policy: LimitPolicy }>(`/api/v1/admin/policies/${encodeURIComponent(policy.id)}`, { method: "PATCH", revision: policy.revision, body: input }); }
  prices(cursor = "") { return this.request<{ data: PriceVersion[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/prices${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`); }
  createPrice(input: { connection_id: string; model_id: string; input_usd_per_million: string; output_usd_per_million: string; source: string; effective_from: string; effective_to: string }) { return this.request<{ price: PriceVersion }>("/api/v1/admin/prices", { method: "POST", body: input }); }
  usageOutbox() { return this.request<{ outbox: OutboxStatus }>("/api/v1/admin/usage/outbox"); }
  previewReprice(input: { connection_id: string; model_id: string; from: string; to: string }) { return this.request<{ preview: { affected_attempts: number; missing_prices: number; delta_usd: string } }>("/api/v1/admin/usage/reprice-preview", { method: "POST", body: input }); }
  applyReprice(input: { connection_id: string; model_id: string; from: string; to: string; idempotency_key: string }) { return this.request<{ result: { affected_attempts: number; missing_prices: number; delta_usd: string } }>("/api/v1/admin/usage/reprice", { method: "POST", body: input }); }
  adjustUsage(input: { attempt_id: string; delta_usd: string; reason: string; idempotency_key: string }) { return this.request<{ restated_cost_usd: string }>("/api/v1/admin/usage/adjustments", { method: "POST", body: input }); }
	reconcileUsage(input: { attempt_id: string; input_tokens: number; output_tokens: number; cost_usd?: string; usage_status: "provider_reported" | "estimated"; reason: string; idempotency_key: string }) { return this.request<{ reconciled: boolean }>("/api/v1/admin/usage/reconciliations", { method: "POST", body: input }); }

  providerTypes() { return this.request<{ data: { id: ProviderConnection["adapter"]; capabilities: string[] }[] }>("/api/v1/providers"); }
  providerPresets() { return this.request<{ data: ProviderPreset[] }>("/api/v1/provider-presets"); }
  connections() { return this.request<{ data: ProviderConnection[] }>("/api/v1/connections"); }
  createConnection(input: { name: string; adapter: ProviderConnection["adapter"]; base_url: string; enabled: boolean; allow_private_network: boolean; timeout_ms: number; preset?: string }) { return this.request<{ connection: ProviderConnection }>("/api/v1/connections", { method: "POST", body: input }); }
  updateConnection(connection: ProviderConnection, input: { name: string; adapter: ProviderConnection["adapter"]; base_url: string; enabled: boolean; allow_private_network: boolean; timeout_ms: number; preset?: string }) { return this.request<{ connection: ProviderConnection }>(`/api/v1/connections/${encodeURIComponent(connection.id)}`, { method: "PATCH", revision: connection.revision, body: input }); }
  putProviderCredential(id: string, input: { credential?: string; external_ref?: string }) { return this.request<void>(`/api/v1/connections/${encodeURIComponent(id)}/credential`, { method: "PUT", body: input }); }
  upstreamModels(connectionID: string) { return this.request<{ data: UpstreamModel[] }>(`/api/v1/connections/${encodeURIComponent(connectionID)}/models`); }
  createUpstreamModel(connectionID: string, input: { upstream_id: string; capabilities: string[] }) { return this.request<{ model: UpstreamModel }>(`/api/v1/connections/${encodeURIComponent(connectionID)}/models`, { method: "POST", body: input }); }
  publicModels() { return this.request<{ data: CatalogModel[] }>("/api/v1/models"); }
  createPublicModel(input: { id: string; label: string; description: string; target_model_id: string; capabilities: string[] }) { return this.request<{ model: PublicModel }>("/api/v1/models", { method: "POST", body: input }); }
  managedModels() { return this.request<{ data: PublicModel[] }>("/api/v1/admin/models"); }
  routeConfig(id: string) { return this.request<{ model: PublicModel; targets: RouteTargetInput[] }>(`/api/v1/admin/models/${encodeURIComponent(id)}/route`); }
  updateRoute(model: PublicModel, input: { strategy: RouteStrategy; free_only: boolean; targets: RouteTargetInput[] }) { return this.request<{ model: PublicModel }>(`/api/v1/admin/models/${encodeURIComponent(model.id)}/route`, { method: "PUT", revision: model.revision, body: input }); }
  previewRoute(id: string, input: RoutePreviewInput) { return this.request<{ route: RoutePlan }>(`/api/v1/admin/models/${encodeURIComponent(id)}/route-preview`, { method: "POST", body: input }); }
  catalog() { return this.request<{ data: CatalogCandidate[]; state: CatalogState }>("/api/v1/admin/catalog"); }
  configureCatalog(input: { source_url: string; refresh_enabled: boolean; refresh_interval_hours: number }) { return this.request<{ state: CatalogState }>("/api/v1/admin/catalog", { method: "PUT", body: input }); }
  refreshCatalog() { return this.request<{ state: CatalogState }>("/api/v1/admin/catalog/refresh", { method: "POST" }); }
  requests(filters: { user_id?: string; key_id?: string; model_id?: string; dialect?: string; cursor?: string } = {}, signal?: AbortSignal) { const query = new URLSearchParams(); for (const [key,value] of Object.entries(filters)) if (value) query.set(key,value); return this.request<{ data: GatewayRequest[]; next_cursor: string; has_more: boolean }>(`/api/v1/requests${query.size ? `?${query}` : ""}`, { signal }); }
  operationSettings() { return this.request<{ settings: OperationSettings }>("/api/v1/admin/settings"); }
  updateOperationSettings(settings: OperationSettings) { return this.request<{ settings: OperationSettings }>("/api/v1/admin/settings", { method: "PATCH", revision: settings.revision, body: settings }); }
  backups() { return this.request<{ data: BackupJob[] }>("/api/v1/admin/backups"); }
  runBackup() { return this.request<{ backup: BackupJob }>("/api/v1/admin/backups", { method: "POST" }); }
  runRetention() { return this.request<{ deleted: Record<string, number> }>("/api/v1/admin/retention", { method: "POST" }); }
	audit(filters: AuditFilters = {}, signal?: AbortSignal) { const query = new URLSearchParams(); for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value); return this.request<{ data: AuditEvent[]; next_cursor: string; has_more: boolean }>(`/api/v1/admin/audit${query.size ? `?${query}` : ""}`, { signal }); }
  diagnostics() { return this.request<{ diagnostics: Diagnostics }>("/api/v1/admin/diagnostics"); }
  exportConfig() { return this.request<unknown>("/api/v1/admin/config/export"); }
  previewConfig(bundle: unknown) { return this.request<{ preview: ConfigPreview }>("/api/v1/admin/config/preview", { method: "POST", body: bundle }); }
  importConfig(bundle: unknown) { return this.request<{ imported: ConfigPreview }>("/api/v1/admin/config/import", { method: "POST", body: bundle }); }
  generate(protocol: "openai" | "anthropic" | "gemini", key: string, model: string, prompt: string, signal?: AbortSignal) {
    if (protocol === "openai") return this.request<unknown>("/api/openai/v1/chat/completions", { method: "POST", authorization: `Bearer ${key}`, redirectOnUnauthorized: false, signal, body: { model, messages: [{ role: "user", content: prompt }] } });
    if (protocol === "anthropic") return this.request<unknown>("/api/anthropic/v1/messages", { method: "POST", anthropicKey: key, redirectOnUnauthorized: false, signal, body: { model, max_tokens: 256, messages: [{ role: "user", content: prompt }] } });
    return this.request<unknown>(`/api/gemini/v1beta/models/${encodeURIComponent(model)}:generateContent`, { method: "POST", geminiKey: key, redirectOnUnauthorized: false, signal, body: { contents: [{ role: "user", parts: [{ text: prompt }] }] } });
  }

  async streamGenerate(protocol: "openai" | "anthropic" | "gemini", key: string, model: string, prompt: string, onChunk: (chunk: string) => void, signal: AbortSignal) {
    const path = protocol === "openai" ? "/api/openai/v1/chat/completions" : protocol === "anthropic" ? "/api/anthropic/v1/messages" : `/api/gemini/v1beta/models/${encodeURIComponent(model)}:streamGenerateContent`;
    assertSameOriginGatewayPath(path);
    const headers = new Headers({ Accept: "text/event-stream", "Content-Type": "application/json" });
    if (protocol === "openai") headers.set("Authorization", `Bearer ${key}`);
    if (protocol === "anthropic") { headers.set("x-api-key", key); headers.set("anthropic-version", "2023-06-01"); }
    if (protocol === "gemini") headers.set("x-goog-api-key", key);
    const body = protocol === "openai" ? { model, stream: true, messages: [{ role: "user", content: prompt }] } : protocol === "anthropic" ? { model, stream: true, max_tokens: 256, messages: [{ role: "user", content: prompt }] } : { contents: [{ role: "user", parts: [{ text: prompt }] }] };
    const response = await this.fetcher(path, { method: "POST", headers, body: JSON.stringify(body), credentials: "same-origin", cache: "no-store", redirect: "error", signal });
    if (!response.ok) throw await gatewayError(response, path, false);
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

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    assertSameOriginGatewayPath(path);
    const method = options.method ?? "GET";
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

export const gatewayAPI = new GatewayAPIClient();
