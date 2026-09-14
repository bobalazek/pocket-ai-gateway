"use client";

import Link from "next/link";
import { useEffect, useState, type FormEvent } from "react";
import { AppShell } from "@/components/app-shell";
import { useGatewayUser } from "@/components/setup-gate";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type CatalogModel, type UpstreamModel } from "@/lib/api-client";

const capabilities = ["chat", "embeddings", "count_tokens"];

export default function ModelsPage() {
  const user = useGatewayUser();
  const manager = user?.role === "owner" || user?.role === "admin";
  const [models, setModels] = useState<CatalogModel[]>([]);
  const [targets, setTargets] = useState<UpstreamModel[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function load() {
    try {
      setModels((await gatewayAPI.publicModels()).data);
      if (manager) {
        const connections = await gatewayAPI.connections();
        const pages = await Promise.all(connections.data.map((item) => gatewayAPI.upstreamModels(item.id)));
        setTargets(pages.flatMap((page) => page.data));
      }
      setError("");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Models are unavailable");
    }
  }
  useEffect(() => { void load(); }, [manager]);
  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    setBusy(true);
    try {
      await gatewayAPI.createPublicModel({ id: String(form.get("id")), label: String(form.get("label")), description: String(form.get("description")), target_model_id: String(form.get("target_model_id")), capabilities: form.getAll("capabilities").map(String) });
      element.reset();
      await load();
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Model could not be published");
    } finally {
      setBusy(false);
    }
  }
  return <AppShell active="Models"><main id="main-content" className="content management-page"><header className="page-header"><div><p className="context">Stable catalog</p><h1>Models</h1><p className="lede">{manager ? "Publish stable client-facing names to fixed native upstream targets." : "Models available within your account grants."}</p></div></header>{error && <p className="form-error" role="alert">{error}</p>}{manager && <Card className="panel"><h2>Publish model</h2><form onSubmit={create}><div className="inline-fields"><Field id="id" label="Public model ID" required /><Field id="label" label="Display name" required /></div><Field id="description" label="Description" /><div className="field"><Label htmlFor="target_model_id">Upstream target</Label><select id="target_model_id" name="target_model_id" className="select" required><option value="">Select a target</option>{targets.map((item) => <option key={item.id} value={item.id}>{item.upstream_id} · {item.connection_id}</option>)}</select></div><fieldset className="scope-grid"><legend>Published capabilities</legend>{capabilities.map((value) => <label key={value}><input name="capabilities" type="checkbox" value={value} /><span>{value.replaceAll("_", " ")}</span></label>)}</fieldset><Button disabled={busy}>Publish model</Button></form></Card>}<section className="section-block"><h2>{models.length} available models</h2><div className="resource-list">{models.map((model) => <Card className="resource-row" key={model.id}><div><strong>{model.label}</strong><code>{model.id}</code><small>{model.adapter} · {model.capabilities.join(", ")}</small></div></Card>)}</div></section>{manager && models.length > 0 && <div className="next-step"><div><strong>Next: create a scoped key</strong><small>Grant only the model, connection, and operations the client needs.</small></div><Link className={buttonVariants()} href="/keys/">Continue to API keys</Link></div>}</main></AppShell>;
}

function Field({ id, label, required = false }: { id: string; label: string; required?: boolean }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} required={required} /></div>;
}
