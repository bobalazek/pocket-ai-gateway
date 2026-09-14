"use client";

import Link from "next/link";
import { useEffect, useState, type FormEvent } from "react";
import { AppShell } from "@/components/app-shell";
import { useGatewayUser } from "@/components/setup-gate";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type CatalogCandidate, type CatalogModel, type CatalogState, type PublicModel, type RoutePlan, type RoutePreviewInput, type RouteStrategy, type RouteTargetInput, type UpstreamModel } from "@/lib/api-client";

const capabilities = ["chat", "embeddings", "count_tokens"];
const strategies: { value: RouteStrategy; label: string }[] = [
  { value: "fixed", label: "Fixed target" }, { value: "ordered_fallback", label: "Ordered fallback" },
  { value: "weighted", label: "Weighted" }, { value: "lowest_cost", label: "Lowest estimated cost" },
  { value: "lowest_latency", label: "Lowest observed latency" },
];

export default function ModelsPage() {
  const user = useGatewayUser();
  const manager = user?.role === "owner" || user?.role === "admin";
  const [models, setModels] = useState<(CatalogModel | PublicModel)[]>([]);
  const [targets, setTargets] = useState<UpstreamModel[]>([]);
  const [routes, setRoutes] = useState<Record<string, RouteTargetInput[]>>({});
  const [previews, setPreviews] = useState<Record<string, { route: RoutePlan; input: RoutePreviewInput }>>({});
  const [catalog, setCatalog] = useState<CatalogCandidate[]>([]);
  const [catalogState, setCatalogState] = useState<CatalogState | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      if (manager) {
        const [managed, connections, catalogResult] = await Promise.all([gatewayAPI.managedModels(), gatewayAPI.connections(), gatewayAPI.catalog()]);
        const upstreamPages = await Promise.all(connections.data.map((item) => gatewayAPI.upstreamModels(item.id)));
        const routePages = await Promise.all(managed.data.map((model) => gatewayAPI.routeConfig(model.id)));
        setModels(managed.data); setTargets(upstreamPages.flatMap((page) => page.data)); setCatalog(catalogResult.data); setCatalogState(catalogResult.state);
        setRoutes(Object.fromEntries(routePages.map((page) => [page.model.id, page.targets])));
      } else setModels((await gatewayAPI.publicModels()).data);
      setError("");
    } catch (failure) { setError(message(failure, "Models are unavailable")); }
  }
  useEffect(() => { void load(); }, [manager]);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const element = event.currentTarget; const form = new FormData(element); setBusy(true);
    try { await gatewayAPI.createPublicModel({ id: String(form.get("id")), label: String(form.get("label")), description: String(form.get("description")), target_model_id: String(form.get("target_model_id")), capabilities: form.getAll("capabilities").map(String) }); element.reset(); await load(); }
    catch (failure) { setError(message(failure, "Model could not be published")); } finally { setBusy(false); }
  }

  async function saveRoute(event: FormEvent<HTMLFormElement>, model: PublicModel) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    const selected = targets.filter((target) => form.get(`target:${target.id}`) === "on").map((target) => ({ upstream_model_id: target.id, priority: Number(form.get(`priority:${target.id}`)), weight: Number(form.get(`weight:${target.id}`)), enabled: true }));
    setBusy(true);
    try { await gatewayAPI.updateRoute(model, { strategy: String(form.get("strategy")) as RouteStrategy, free_only: form.get("free_only") === "on", targets: selected }); await load(); }
    catch (failure) { setError(message(failure, "Route could not be saved")); } finally { setBusy(false); }
  }

  async function preview(event: FormEvent<HTMLFormElement>, model: PublicModel) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    const input = { operation: String(form.get("operation")), streaming: form.get("streaming") === "on", estimated_input_tokens: Number(form.get("estimated_input_tokens")), estimated_output_tokens: Number(form.get("estimated_output_tokens")) };
    setBusy(true);
    try { const result = await gatewayAPI.previewRoute(model.id, input); setPreviews((current) => ({ ...current, [model.id]: { route: result.route, input } })); }
    catch (failure) { setError(message(failure, "Route preview failed")); } finally { setBusy(false); }
  }

  async function saveCatalog(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget); setBusy(true);
    try { const result = await gatewayAPI.configureCatalog({ source_url: String(form.get("source_url")), refresh_enabled: form.get("refresh_enabled") === "on", refresh_interval_hours: Number(form.get("refresh_interval_hours")) }); setCatalogState(result.state); }
    catch (failure) { setError(message(failure, "Catalog settings could not be saved")); } finally { setBusy(false); }
  }
  async function refreshCatalog() { setBusy(true); try { await gatewayAPI.refreshCatalog(); await load(); } catch (failure) { setError(message(failure, "Catalog refresh failed")); } finally { setBusy(false); } }

  return <AppShell active="Models"><main id="main-content" className="content management-page">
    <header className="page-header"><div><p className="context">Stable catalog</p><h1>Models</h1><p className="lede">{manager ? "Publish stable names, choose eligible targets, and preview every routing decision." : "Models available within your account grants."}</p></div></header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {manager && <Card className="panel"><h2>Publish model</h2><form onSubmit={create}><div className="inline-fields"><Field id="id" label="Public model ID" required /><Field id="label" label="Display name" required /></div><Field id="description" label="Description" /><div className="field"><Label htmlFor="target_model_id">Initial upstream target</Label><select id="target_model_id" name="target_model_id" className="select" required><option value="">Select a target</option>{targets.map((item) => <option key={item.id} value={item.id}>{item.upstream_id} · {item.connection_id}</option>)}</select></div><fieldset className="scope-grid"><legend>Published capabilities</legend>{capabilities.map((value) => <label key={value}><input name="capabilities" type="checkbox" value={value} /><span>{value.replaceAll("_", " ")}</span></label>)}</fieldset><Button disabled={busy}>Publish model</Button></form></Card>}
    <section className="section-block"><h2>{models.length} available models</h2><div className="resource-list">{models.map((item) => {
      const model = item as PublicModel; const configured = routes[item.id] ?? []; const embeddingModel = item.capabilities.includes("embeddings");
      return <Card className="panel" key={item.id}><div className="resource-row-main"><div><strong>{item.label}</strong><code>{item.id}</code><small>{item.adapter} · {item.capabilities.join(", ")}{manager ? ` · ${model.routing_strategy.replaceAll("_", " ")}` : ""}</small></div></div>
        {manager && <form className="route-preview-controls" onSubmit={(event) => preview(event, model)}><div className="field"><Label htmlFor={`preview-operation-${model.id}`}>Operation</Label><Input id={`preview-operation-${model.id}`} name="operation" defaultValue="chat/completions" required /></div><div className="field"><Label htmlFor={`preview-input-${model.id}`}>Input tokens</Label><Input id={`preview-input-${model.id}`} name="estimated_input_tokens" type="number" min="0" defaultValue="1000" required /></div><div className="field"><Label htmlFor={`preview-output-${model.id}`}>Output tokens</Label><Input id={`preview-output-${model.id}`} name="estimated_output_tokens" type="number" min="0" defaultValue="500" required /></div><label className="checkbox-row"><input name="streaming" type="checkbox" /> Streaming</label><Button variant="outline" disabled={busy}>Preview route</Button></form>}
        {manager && <details className="grant-editor"><summary>Routing strategy and targets</summary><form onSubmit={(event) => saveRoute(event, model)}><div className="inline-fields"><div className="field"><Label htmlFor={`strategy-${model.id}`}>Strategy</Label><select id={`strategy-${model.id}`} name="strategy" className="select" defaultValue={model.routing_strategy}>{strategies.filter((strategy) => !embeddingModel || strategy.value === "fixed").map((strategy) => <option key={strategy.value} value={strategy.value}>{strategy.label}</option>)}</select></div><label className="checkbox-row"><input name="free_only" type="checkbox" defaultChecked={model.free_only} /> Require verified zero provider pricing</label></div><div className="resource-list">{targets.map((target, index) => { const current = configured.find((entry) => entry.upstream_model_id === target.id); return <div className="route-target" key={target.id}><label className="checkbox-row"><input name={`target:${target.id}`} type="checkbox" defaultChecked={Boolean(current)} disabled={embeddingModel && !current} /> {target.upstream_id}</label><Input aria-label={`${target.upstream_id} priority`} name={`priority:${target.id}`} type="number" min="1" max="1000" defaultValue={String(current?.priority ?? index + 1)} /><Input aria-label={`${target.upstream_id} weight`} name={`weight:${target.id}`} type="number" min="1" max="10000" defaultValue={String(current?.weight ?? 1)} /></div>})}</div><p className="help-text">{embeddingModel ? "Embedding models keep one fixed target so their vector space cannot change." : "Eligibility and grants are checked before scoring. Priority is also the fallback order."} Free-only prices must have manager-recorded zero rates verified within 24 hours.</p><Button disabled={busy}>Save route</Button></form></details>}
        {previews[item.id] && <div className="route-preview" role="status"><strong>Selected: {previews[item.id].route.targets[0]?.upstream_id ?? "none"}</strong><small>{previews[item.id].route.selection_reason}</small><small>{previews[item.id].input.operation} · {previews[item.id].input.streaming ? "streaming" : "non-streaming"} · {previews[item.id].input.estimated_input_tokens} input / {previews[item.id].input.estimated_output_tokens} output tokens</small>{previews[item.id].route.rejected.map((entry) => <small key={`${entry.connection_id}:${entry.upstream_model_id}`}>{entry.upstream_model_id}: {entry.reason.replaceAll("_", " ")}</small>)}</div>}
      </Card>; })}</div></section>
    {manager && catalogState && <Card className="panel"><h2>Provider catalog</h2><p className="help-text">Refresh imports bounded metadata candidates only. Review a candidate below, then <Link href="/providers/">add its model to the matching provider</Link> and publish the stable public ID above. Local model and price entries remain authoritative.</p><form onSubmit={saveCatalog}><Field id="source_url" label="GitHub catalog JSON URL" type="url" defaultValue={catalogState.source_url} /><div className="inline-fields"><Field id="refresh_interval_hours" label="Refresh interval (hours)" type="number" defaultValue={String(catalogState.refresh_interval_hours)} required /><label className="checkbox-row"><input name="refresh_enabled" type="checkbox" defaultChecked={catalogState.refresh_enabled} /> Enable scheduled refresh</label></div><div className="button-row"><Button disabled={busy}>Save catalog settings</Button><Button type="button" variant="outline" disabled={busy || !catalogState.source_url} onClick={refreshCatalog}>Refresh now</Button></div></form><small>{catalog.length} candidates · version {catalogState.source_version || "not loaded"}{catalogState.last_error ? ` · ${catalogState.last_error}` : ""}</small>{catalog.length > 0 && <div className="catalog-candidates" aria-label="Catalog candidates">{catalog.slice(0, 50).map((candidate) => <div className="catalog-candidate" key={`${candidate.provider}:${candidate.model_id}`}><div><strong>{candidate.label}</strong><code>{candidate.model_id}</code><small>{candidate.provider} · {candidate.capabilities.join(", ")}</small></div><div><strong>{candidate.free ? "Verified free" : formatCatalogPrice(candidate)}</strong><small>Source {candidate.source_version} · {new Date(candidate.discovered_at).toLocaleString()}</small></div></div>)}{catalog.length > 50 && <small>Showing the first 50 candidates.</small>}</div>}</Card>}
    {manager && models.length > 0 && <div className="next-step"><div><strong>Next: create a scoped key</strong><small>Grant every connection that a model route may select.</small></div><Link className={buttonVariants()} href="/keys/">Continue to API keys</Link></div>}
  </main></AppShell>;
}

function Field({ id, label, type = "text", defaultValue, required = false }: { id: string; label: string; type?: string; defaultValue?: string; required?: boolean }) { return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} type={type} defaultValue={defaultValue} required={required} /></div>; }
function message(error: unknown, fallback: string) { return error instanceof GatewayAPIError ? error.message : fallback; }
function formatCatalogPrice(candidate: CatalogCandidate) { if (candidate.input_nanos_per_million === undefined || candidate.output_nanos_per_million === undefined) return "Price unknown"; return `$${candidate.input_nanos_per_million / 1_000_000_000} in · $${candidate.output_nanos_per_million / 1_000_000_000} out / 1M`; }
