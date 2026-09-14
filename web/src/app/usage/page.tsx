"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { Area, AreaChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";
import { AppShell } from "@/components/app-shell";
import { useGatewayUser } from "@/components/setup-gate";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ChartContainer } from "@/components/ui/chart";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type EffectiveLimit, type LimitPolicy, type OutboxStatus, type PriceVersion, type UnresolvedAttempt, type UsageFilters, type UsageSummary } from "@/lib/api-client";

const emptyUsage: UsageSummary = { from: "", to: "", requests: 0, attempts: 0, input_tokens: 0, output_tokens: 0, known_cost_usd: "0", estimated_cost_usd: "0", as_recorded_cost_usd: "0", restatement_delta_usd: "0", unknown_attempts: 0, points: [] };
const datetime = (value: FormDataEntryValue | null) => value ? new Date(String(value)).toISOString() : "";
const localDatetime = (value?: string) => {
  if (!value) return ""; const date = new Date(value); if (Number.isNaN(date.getTime())) return ""; const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
};
const failureText = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? `${error.message}${error.metric ? ` (${error.metric})` : ""}${error.retryAfter ? ` Retry in ${error.retryAfter}s.` : ""}` : fallback;
const isRFC3339 = (value: string) => /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && !Number.isNaN(new Date(value).getTime());
const readFilters = (): UsageFilters => {
  if (typeof window === "undefined") return {};
  const query = new URLSearchParams(window.location.search); const filters: UsageFilters = {};
  for (const key of ["from", "to", "user_id", "key_id", "model_id", "connection_id"] as const) { const value = query.get(key); if (value && (!(key === "from" || key === "to") || isRFC3339(value))) filters[key] = value; }
  return filters;
};
const kinds = {
  quota_requests: ["requests", "quota"], quota_tokens: ["tokens", "quota"], quota_spend: ["spend", "quota"],
  bucket_requests: ["requests", "token_bucket"], bucket_tokens: ["tokens", "token_bucket"], window_requests: ["requests", "fixed_window"], window_tokens: ["tokens", "fixed_window"],
  concurrency: ["concurrency", "concurrency"], body: ["body_bytes", "ceiling"], output: ["output_tokens", "ceiling"], batch: ["batch_items", "ceiling"],
} as const;

export default function UsagePage() {
  const user = useGatewayUser();
  const isOwner = user?.role === "owner";
  const canManage = isOwner || user?.role === "admin";
  const [usage, setUsage] = useState(emptyUsage);
  const [unresolved, setUnresolved] = useState<UnresolvedAttempt[]>([]);
  const [policies, setPolicies] = useState<LimitPolicy[]>([]);
  const [prices, setPrices] = useState<PriceVersion[]>([]);
  const [policyCursor, setPolicyCursor] = useState("");
  const [priceCursor, setPriceCursor] = useState("");
  const [unresolvedCursor, setUnresolvedCursor] = useState("");
  const [outbox, setOutbox] = useState<OutboxStatus | null>(null);
  const [limits, setLimits] = useState<EffectiveLimit[]>([]);
  const [filters, setFilters] = useState<UsageFilters>({});
  const [filtersReady, setFiltersReady] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const loadGeneration = useRef(0);
  const [reprice, setReprice] = useState<{ input: { connection_id: string; model_id: string; from: string; to: string }; key: string; text: string } | null>(null);
  const [pricePreview, setPricePreview] = useState<{ connection_id: string; model_id: string; input_usd_per_million: string; output_usd_per_million: string; source: string; effective_from: string; effective_to: string } | null>(null);
  const [adjustment, setAdjustment] = useState<{ attempt_id: string; delta_usd: string; reason: string; idempotency_key: string } | null>(null);
  const [reconciliation, setReconciliation] = useState<{ attempt_id: string; input_tokens: number; output_tokens: number; cost_usd?: string; usage_status: "provider_reported" | "estimated"; reason: string; idempotency_key: string } | null>(null);

  async function load() {
    const generation = ++loadGeneration.current;
    setBusy(false);
    try {
      const [summary, unknown, policyList] = await Promise.all([gatewayAPI.usage(filters), gatewayAPI.unresolvedUsage(filters), gatewayAPI.policies()]);
      if (generation !== loadGeneration.current) return;
      setUsage(summary.usage); setUnresolved(unknown.data); setUnresolvedCursor(unknown.next_cursor); setPolicies(policyList.data); setPolicyCursor(policyList.next_cursor);
      if (canManage) {
        const [priceList, outboxState] = await Promise.all([gatewayAPI.prices(), gatewayAPI.usageOutbox()]);
        if (generation !== loadGeneration.current) return;
        setPrices(priceList.data); setPriceCursor(priceList.next_cursor); setOutbox(outboxState.outbox);
      }
      if (generation === loadGeneration.current) setError("");
    } catch (failure) { if (generation === loadGeneration.current) setError(failureText(failure, "Usage data is unavailable")); }
  }
  useEffect(() => { const sync = () => { const next = readFilters(); const query = new URLSearchParams(next as Record<string, string>); const canonical = query.size ? `?${query}` : ""; if (canonical !== window.location.search) window.history.replaceState(null, "", `${window.location.pathname}${canonical}`); setFilters(next); setFiltersReady(true); }; sync(); window.addEventListener("popstate", sync); return () => window.removeEventListener("popstate", sync); }, []);
  useEffect(() => { if (filtersReady) void load(); }, [canManage, filters, filtersReady]);

  function showFailure(failure: unknown, fallback: string) { setError(failureText(failure, fallback)); }
  async function loadMore(kind: "policies" | "prices" | "unresolved") {
    const generation = loadGeneration.current; setBusy(true);
    try {
      if (kind === "policies") { const page = await gatewayAPI.policies(policyCursor); if (generation !== loadGeneration.current) return; setPolicies((items) => [...items, ...page.data]); setPolicyCursor(page.next_cursor); }
      if (kind === "prices") { const page = await gatewayAPI.prices(priceCursor); if (generation !== loadGeneration.current) return; setPrices((items) => [...items, ...page.data]); setPriceCursor(page.next_cursor); }
      if (kind === "unresolved") { const page = await gatewayAPI.unresolvedUsage(filters, unresolvedCursor); if (generation !== loadGeneration.current) return; setUnresolved((items) => [...items, ...page.data]); setUnresolvedCursor(page.next_cursor); }
    } catch (failure) { if (generation === loadGeneration.current) showFailure(failure, "The next page could not be loaded"); } finally { if (generation === loadGeneration.current) setBusy(false); }
  }
  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget); const next: UsageFilters = {};
    for (const key of ["from", "to", "user_id", "key_id", "model_id", "connection_id"] as const) {
      const value = String(form.get(key) ?? ""); if (value) next[key] = key === "from" || key === "to" ? datetime(value) : value;
    }
    setFilters(next); const query = new URLSearchParams(next as Record<string, string>); window.history.replaceState(null, "", query.size ? `?${query}` : window.location.pathname);
  }
  async function inspectLimits(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget); setBusy(true);
    try { setLimits((await gatewayAPI.effectiveLimits(String(form.get("key_id")), String(form.get("connection_id")))).data); } catch (failure) { showFailure(failure, "Effective limits are unavailable"); } finally { setBusy(false); }
  }
  async function createPolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const element = event.currentTarget; const form = new FormData(element); const [metric, algorithm] = kinds[String(form.get("kind")) as keyof typeof kinds]; setBusy(true);
    try {
      const limit = String(form.get("limit")); const limitUnits = metric === "spend" ? 1 : Number(limit);
      if (!Number.isSafeInteger(limitUnits) || limitUnits < 1) throw new Error("Non-spend limits must be positive whole numbers");
      await gatewayAPI.createPolicy({ scope_kind: String(form.get("scope_kind")) as LimitPolicy["scope_kind"], scope_id: String(form.get("scope_id")), metric, algorithm, period: algorithm === "quota" ? String(form.get("period")) as LimitPolicy["period"] : "", window_seconds: algorithm === "fixed_window" ? Number(form.get("window_seconds")) : 0, limit_units: limitUnits, limit_usd: metric === "spend" ? limit : "", refill_units: algorithm === "token_bucket" ? Number(form.get("refill_units")) : 0, refill_interval_ms: algorithm === "token_bucket" ? Number(form.get("refill_interval_ms")) : 0, enabled: true });
      element.reset(); await load();
    } catch (failure) { showFailure(failure, "Policy could not be created"); } finally { setBusy(false); }
  }
  async function changePolicy(policy: LimitPolicy, value: string, enabled: boolean) {
    if (!window.confirm(`${enabled === policy.enabled ? "Change" : enabled ? "Enable" : "Disable"} ${policy.metric} policy${enabled === policy.enabled ? ` to ${value}` : ""}?`)) return;
    setBusy(true); try { await gatewayAPI.updatePolicy(policy, { limit_units: policy.metric === "spend" ? 1 : Number(value), limit_usd: value, enabled }); await load(); } catch (failure) { showFailure(failure, "Policy could not be updated"); } finally { setBusy(false); }
  }
  async function createPrice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const element = event.currentTarget;
    if (!pricePreview) { const form = new FormData(element); setPricePreview({ connection_id: String(form.get("connection_id")), model_id: String(form.get("model_id")), input_usd_per_million: String(form.get("input_price")), output_usd_per_million: String(form.get("output_price")), source: String(form.get("source")), effective_from: datetime(form.get("effective_from")), effective_to: datetime(form.get("effective_to")) }); return; }
    setBusy(true); try { await gatewayAPI.createPrice(pricePreview); setPricePreview(null); element.reset(); await load(); } catch (failure) { showFailure(failure, "Price could not be created"); } finally { setBusy(false); }
  }
  async function previewReprice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget); const input = { connection_id: String(form.get("connection_id")), model_id: String(form.get("model_id")), from: datetime(form.get("from")), to: datetime(form.get("to")) }; setBusy(true);
    try { const result = await gatewayAPI.previewReprice(input); setReprice({ input, key: crypto.randomUUID(), text: `${input.connection_id} / ${input.model_id}, ${new Date(input.from).toLocaleString()}–${new Date(input.to).toLocaleString()}: ${result.preview.affected_attempts} attempts, ${result.preview.missing_prices} missing, $${result.preview.delta_usd} change` }); } catch (failure) { showFailure(failure, "Preview failed"); } finally { setBusy(false); }
  }
  async function applyReprice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!reprice || busyRef.current) return; busyRef.current = true; setBusy(true);
    try { await gatewayAPI.applyReprice({ ...reprice.input, idempotency_key: reprice.key }); setReprice(null); setNotice("Repricing applied."); await load(); } catch (failure) { showFailure(failure, "Repricing failed"); } finally { busyRef.current = false; setBusy(false); }
  }
  async function adjust(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (busyRef.current) return;
    if (!adjustment) { const form = new FormData(event.currentTarget); setAdjustment({ attempt_id: String(form.get("attempt_id")), delta_usd: String(form.get("delta_usd")), reason: String(form.get("reason")), idempotency_key: crypto.randomUUID() }); return; }
    busyRef.current = true; setBusy(true); try { await gatewayAPI.adjustUsage(adjustment); setAdjustment(null); setNotice("Cost adjusted."); event.currentTarget.reset(); await load(); } catch (failure) { showFailure(failure, "Adjustment failed"); } finally { busyRef.current = false; setBusy(false); }
  }
  async function reconcile(event: FormEvent<HTMLFormElement>, attempt: UnresolvedAttempt) {
    event.preventDefault();
    if (!reconciliation || reconciliation.attempt_id !== attempt.id) {
      const form = new FormData(event.currentTarget);
      setReconciliation({ attempt_id: attempt.id, input_tokens: Number(form.get("input_tokens")), output_tokens: Number(form.get("output_tokens")), cost_usd: String(form.get("cost_usd")) || undefined, usage_status: String(form.get("usage_status")) as "provider_reported" | "estimated", reason: String(form.get("reason")), idempotency_key: crypto.randomUUID() });
      return;
    }
    if (busyRef.current) return; busyRef.current = true; setBusy(true);
    try { await gatewayAPI.reconcileUsage(reconciliation); setReconciliation(null); setNotice("Usage reconciled."); await load(); } catch (failure) { showFailure(failure, "Reconciliation failed"); } finally { busyRef.current = false; setBusy(false); }
  }

  const chartData = usage.points.map((point) => ({ ...point, tokens: point.input_tokens + point.output_tokens }));
  return <AppShell active="Usage"><main id="main-content" className="content management-page">
    <header className="page-header"><div><p className="context">Accounting</p><h1>Usage and limits</h1><p className="lede">Local usage, enforceable limits, and effective-dated prices.</p></div></header>
    {error && <p className="form-error" role="alert">{error}</p>}{notice && <p className="form-success" role="status">{notice}</p>}
    <Card className="panel"><h2>Filter usage</h2><form className="filter-grid" key={JSON.stringify(filters)} onSubmit={applyFilters}><Field id="from" label="From" type="datetime-local" defaultValue={localDatetime(filters.from)}/><Field id="to" label="To" type="datetime-local" defaultValue={localDatetime(filters.to)}/>{canManage && <Field id="user_id" label="User ID" defaultValue={filters.user_id}/>}<Field id="key_id" label="Key ID" defaultValue={filters.key_id}/><Field id="model_id" label="Model ID" defaultValue={filters.model_id}/><Field id="connection_id" label="Connection ID" defaultValue={filters.connection_id}/><Button>Apply</Button></form></Card>
    <section className="metric-grid" aria-label="Usage totals"><Metric label="Requests" value={usage.requests.toLocaleString()}/><Metric label="Tokens" value={(usage.input_tokens + usage.output_tokens).toLocaleString()}/><Metric label="Current cost" value={`$${usage.known_cost_usd}`}/><Metric label="Needs review" value={usage.unknown_attempts.toLocaleString()}/></section>
    <Card className="panel"><p className="context">Cost provenance</p><div className="metric-grid"><Metric label="Estimated" value={`$${usage.estimated_cost_usd}`}/><Metric label="As recorded" value={`$${usage.as_recorded_cost_usd}`}/><Metric label="Restatement change" value={`$${usage.restatement_delta_usd}`}/></div></Card>
    <Card className="panel chart-panel"><h2>Tokens by day</h2>{chartData.length ? <><ChartContainer label="Daily token usage"><AreaChart responsive data={chartData}><CartesianGrid vertical={false}/><XAxis dataKey="date"/><YAxis width={52}/><Tooltip/><Area dataKey="tokens" stroke="var(--accent)" fill="var(--accent-soft)"/></AreaChart></ChartContainer><div className="table-wrap" tabIndex={0} role="region" aria-label="Scrollable daily usage table"><table><thead><tr><th>Date</th><th>Requests</th><th>Input</th><th>Output</th><th>Cost</th><th>Unknown</th></tr></thead><tbody>{usage.points.map((point) => <tr key={point.date}><td>{point.date}</td><td>{point.requests}</td><td>{point.input_tokens}</td><td>{point.output_tokens}</td><td>${point.known_cost_usd}</td><td>{point.unknown_attempts}</td></tr>)}</tbody></table></div></> : <p className="empty-copy">No settled usage in this period.</p>}</Card>
    <section className="section-block"><h2>Limit policies</h2><div className="resource-list">{policies.map((policy) => <Card className="resource-row" key={policy.id}><div><strong>{policy.metric.replaceAll("_", " ")} · {policy.limit_usd ? `$${policy.limit_usd}` : policy.limit_units.toLocaleString()}</strong><small>{policy.scope_kind}{policy.scope_id ? ` ${policy.scope_id}` : ""} · {policy.algorithm}{policy.period ? ` / ${policy.period}` : ""} · {policy.enabled ? "enforcing" : "disabled"}</small></div>{canManage && (policy.scope_kind !== "instance" || isOwner) && <div className="row-actions"><form className="limit-edit" onSubmit={(event) => { event.preventDefault(); void changePolicy(policy, String(new FormData(event.currentTarget).get("limit")), policy.enabled); }}><Input aria-label={`New ${policy.metric} limit`} name="limit" defaultValue={policy.limit_usd ?? policy.limit_units}/><Button variant="outline" disabled={busy}>Save</Button></form><Button variant="outline" disabled={busy} onClick={() => changePolicy(policy, policy.limit_usd ?? String(policy.limit_units), !policy.enabled)}>{policy.enabled ? "Disable" : "Enable"}</Button></div>}</Card>)}</div>{policyCursor && <Button className="section-block" variant="outline" disabled={busy} onClick={() => loadMore("policies")}>Load more policies</Button>}</section>
    <Card className="panel"><h2>Inspect effective limits</h2><form onSubmit={inspectLimits}><div className="inline-fields"><Field id="limits_key" name="key_id" label="API key ID" required/><Field id="limits_connection" name="connection_id" label="Connection ID"/></div><Button disabled={busy}>Inspect</Button></form>{limits.length > 0 && <div className="resource-list section-block">{limits.map((limit) => <div className="resource-row" key={limit.policy_id}><div><strong>{limit.scope_kind} {limit.metric} · cap {limit.limit_usd ? `$${limit.limit_usd}` : limit.limit_units.toLocaleString()}</strong><small>{limit.consumed_usd ? `$${limit.consumed_usd}` : limit.consumed_units.toLocaleString()} consumed · {limit.reserved_usd ? `$${limit.reserved_usd}` : limit.reserved_units.toLocaleString()} reserved · {limit.remaining_usd ? `$${limit.remaining_usd}` : limit.remaining_units.toLocaleString()} remaining · {limit.algorithm}{limit.period ? ` / ${limit.period}` : ""}{limit.resets_at ? ` · resets ${new Date(limit.resets_at).toLocaleString()}` : ""}</small></div></div>)}</div>}</Card>
    {canManage && <PolicyForm isOwner={isOwner} busy={busy} onSubmit={createPolicy}/>} {canManage && <PriceSection prices={prices} outbox={outbox} cursor={priceCursor} preview={pricePreview} busy={busy} onLoadMore={() => loadMore("prices")} onSubmit={createPrice} onCancel={() => setPricePreview(null)}/>} {isOwner && <RepriceForm preview={reprice} busy={busy} onPreview={previewReprice} onApply={applyReprice} onCancel={() => setReprice(null)}/>} {canManage && <AdjustmentForm preview={adjustment} busy={busy} onSubmit={adjust} onCancel={() => setAdjustment(null)}/>} 
    <section className="section-block"><h2>Unknown usage</h2>{unresolved.length ? <div className="resource-list">{unresolved.map((attempt) => <UnknownRow key={attempt.id} attempt={attempt} preview={reconciliation?.attempt_id === attempt.id ? reconciliation : null} canManage={canManage} busy={busy} onSubmit={reconcile} onCancel={() => setReconciliation(null)}/>)}</div> : <p className="empty-copy">No unresolved attempts.</p>}{unresolvedCursor && <Button className="section-block" variant="outline" disabled={busy} onClick={() => loadMore("unresolved")}>Load more unresolved usage</Button>}</section>
  </main></AppShell>;
}

function Field({ id, name, label, type = "text", required = false, disabled = false, defaultValue }: { id: string; name?: string; label: string; type?: string; required?: boolean; disabled?: boolean; defaultValue?: string }) { return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={name ?? id} type={type} required={required} disabled={disabled} defaultValue={defaultValue}/></div>; }
function Metric({ label, value }: { label: string; value: string }) { return <Card><small>{label}</small><strong>{value}</strong></Card>; }
function PolicyForm({ isOwner, busy, onSubmit }: { isOwner: boolean; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) { return <Card className="panel"><h2>Create policy</h2><form onSubmit={onSubmit}><div className="inline-fields"><div className="field"><Label htmlFor="scope_kind">Scope</Label><select className="select" id="scope_kind" name="scope_kind">{isOwner && <option value="instance">Instance</option>}<option value="user">User</option><option value="key">API key</option><option value="connection">Connection</option></select></div><Field id="scope_id" label="Scope ID"/></div><div className="inline-fields"><div className="field"><Label htmlFor="kind">Policy type</Label><select className="select" id="kind" name="kind">{Object.keys(kinds).map((kind) => <option key={kind} value={kind}>{kind.replaceAll("_", " ")}</option>)}</select></div><div className="field"><Label htmlFor="limit">Limit</Label><Input id="limit" name="limit" type="number" step="any" min="0.000000001" defaultValue="100" required/></div></div><div className="inline-fields"><div className="field"><Label htmlFor="period">Quota period</Label><select className="select" id="period" name="period" defaultValue="day"><option>hour</option><option>day</option><option>week</option><option>month</option><option>lifetime</option></select></div><Field id="window_seconds" label="Window seconds" type="number"/></div><div className="inline-fields"><Field id="refill_units" label="Refill units" type="number"/><Field id="refill_interval_ms" label="Refill interval (ms)" type="number"/></div><Button disabled={busy}>Create policy</Button></form></Card>; }
function PriceSection({ prices, outbox, cursor, preview, busy, onLoadMore, onSubmit, onCancel }: { prices: PriceVersion[]; outbox: OutboxStatus | null; cursor: string; preview: { connection_id: string; model_id: string; input_usd_per_million: string; output_usd_per_million: string; source: string; effective_from: string; effective_to: string } | null; busy: boolean; onLoadMore: () => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void; onCancel: () => void }) { return <section className="section-block"><div className="section-heading"><div><p className="context">Price provenance</p><h2>Effective prices</h2></div><small>{(outbox?.pending_events ?? 0) + (outbox?.reserved_events ?? 0)} events pending or reserved</small></div><div className="resource-list">{prices.map((price) => <Card className="resource-row" key={price.id}><div><strong>{price.model_id}</strong><small>{price.connection_id} · ${price.input_usd_per_million} / ${price.output_usd_per_million} per million · {price.source}</small><small>{new Date(price.effective_from).toLocaleString()} → {price.effective_to ? new Date(price.effective_to).toLocaleString() : "current"}</small></div></Card>)}</div>{cursor && <Button className="section-block" variant="outline" disabled={busy} onClick={onLoadMore}>Load more prices</Button>}<Card className="panel section-block"><h2>Add price version</h2><form onSubmit={onSubmit}><div className="inline-fields"><Field id="price_connection" name="connection_id" label="Connection ID" required disabled={!!preview}/><Field id="price_model" name="model_id" label="Model ID" required disabled={!!preview}/></div><div className="inline-fields"><Field id="input_price" label="Input USD / million" required disabled={!!preview}/><Field id="output_price" label="Output USD / million" required disabled={!!preview}/></div><Field id="source" label="Source" required disabled={!!preview}/><div className="inline-fields"><Field id="effective_from" label="Effective from" type="datetime-local" required disabled={!!preview}/><Field id="effective_to" label="Effective to" type="datetime-local" disabled={!!preview}/></div>{preview && <p className="form-success" role="status">Confirm {preview.connection_id} / {preview.model_id}: ${preview.input_usd_per_million} input, ${preview.output_usd_per_million} output · {preview.source} · from {new Date(preview.effective_from).toLocaleString()} to {preview.effective_to ? new Date(preview.effective_to).toLocaleString() : "current"}?</p>}<div className="row-actions"><Button disabled={busy}>{preview ? "Confirm price" : "Preview price"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div></form></Card></section>; }
function RepriceForm({ preview, busy, onPreview, onApply, onCancel }: { preview: object | null; busy: boolean; onPreview: (event: FormEvent<HTMLFormElement>) => void; onApply: (event: FormEvent<HTMLFormElement>) => void; onCancel: () => void }) { const item = preview as { text: string } | null; return <Card className="panel"><h2>Historical repricing</h2><form onSubmit={item ? onApply : onPreview}><div className="inline-fields"><Field id="reprice_connection" name="connection_id" label="Connection ID" required disabled={!!item}/><Field id="reprice_model" name="model_id" label="Model ID" required disabled={!!item}/></div><div className="inline-fields"><Field id="reprice_from" name="from" label="From" type="datetime-local" required disabled={!!item}/><Field id="reprice_to" name="to" label="To" type="datetime-local" required disabled={!!item}/></div>{item && <p className="form-success" role="status">{item.text}</p>}<div className="row-actions"><Button disabled={busy}>{item ? "Apply repricing" : "Preview repricing"}</Button>{item && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div></form></Card>; }
function AdjustmentForm({ preview, busy, onSubmit, onCancel }: { preview: { attempt_id: string; delta_usd: string } | null; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>) => void; onCancel: () => void }) { return <Card className="panel"><h2>Cost adjustment</h2><form onSubmit={onSubmit}><Field id="attempt_id" label="Attempt ID" required disabled={!!preview}/><Field id="delta_usd" label="USD change" required disabled={!!preview}/><Field id="reason" label="Reason" required disabled={!!preview}/>{preview && <p className="form-success" role="status">Apply {preview.delta_usd} USD to {preview.attempt_id}?</p>}<div className="row-actions"><Button disabled={busy}>{preview ? "Confirm adjustment" : "Preview adjustment"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div></form></Card>; }
function UnknownRow({ attempt, preview, canManage, busy, onSubmit, onCancel }: { attempt: UnresolvedAttempt; preview: { input_tokens: number; output_tokens: number; cost_usd?: string } | null; canManage: boolean; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>, attempt: UnresolvedAttempt) => void; onCancel: () => void }) { return <Card className="panel"><strong>{attempt.model_id}</strong><small>{attempt.id} · {attempt.connection_id} · {new Date(attempt.started_at).toLocaleString()}</small>{canManage && <form className="section-block" onChange={() => preview && onCancel()} onSubmit={(event) => onSubmit(event, attempt)}><div className="inline-fields"><Field id={`input-${attempt.id}`} name="input_tokens" label="Input tokens" type="number" required/><Field id={`output-${attempt.id}`} name="output_tokens" label="Output tokens" type="number" required/></div><div className="inline-fields"><Field id={`cost-${attempt.id}`} name="cost_usd" label="Known cost USD (optional)"/><div className="field"><Label htmlFor={`status-${attempt.id}`}>Source</Label><select id={`status-${attempt.id}`} className="select" name="usage_status"><option value="provider_reported">Provider reported</option><option value="estimated">Estimated</option></select></div></div><Field id={`reason-${attempt.id}`} name="reason" label="Reason" required/>{preview && <p className="form-success" role="status">Confirm {preview.input_tokens + preview.output_tokens} tokens{preview.cost_usd ? ` and $${preview.cost_usd}` : ""}?</p>}<div className="row-actions"><Button disabled={busy}>{preview ? "Confirm reconciliation" : "Preview reconciliation"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div></form>}</Card>; }
