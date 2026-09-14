"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type GatewayRequest } from "@/lib/api-client";

type Filters = { user_id?: string; key_id?: string; model_id?: string; dialect?: string; cursor?: string };
const read = (): Filters => { const query = new URLSearchParams(window.location.search); const result: Filters = {}; for (const key of ["user_id", "key_id", "model_id", "dialect", "cursor"] as const) { const value = query.get(key); if (value) result[key] = value; } return result; };

export default function RequestsPage() {
  const active = useRef<AbortController | null>(null);
  const [items, setItems] = useState<GatewayRequest[]>([]);
  const [filters, setFilters] = useState<Filters>({});
  const [next, setNext] = useState("");
  const [error, setError] = useState("");
  async function load(value: Filters) {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    try {
      const page = await gatewayAPI.requests(value, controller.signal);
      if (controller.signal.aborted) return;
      setItems(page.data); setNext(page.next_cursor); setError("");
    } catch (failure) {
      if (!controller.signal.aborted) setError(failure instanceof GatewayAPIError ? failure.message : "Requests are unavailable");
    }
  }
  useEffect(() => { const sync = () => { const value = read(); setFilters(value); void load(value); }; sync(); window.addEventListener("popstate", sync); return () => { active.current?.abort(); window.removeEventListener("popstate", sync); }; }, []);
  function navigate(value: Filters) { const query = new URLSearchParams(value as Record<string, string>); window.history.pushState({}, "", query.size ? `?${query}` : window.location.pathname); setFilters(value); void load(value); }
  function apply(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); const value: Filters = {}; for (const key of ["user_id", "key_id", "model_id", "dialect"] as const) { const field = String(form.get(key) ?? ""); if (field) value[key] = field; } navigate(value); }
  return <AppShell active="Requests"><main id="main-content" className="content management-page"><header className="page-header"><div><p className="context">Inference history</p><h1>Requests</h1><p className="lede">Metadata and accounting state. Request content is not captured.</p></div></header>{error && <p className="form-error" role="alert">{error}</p>}<Card className="panel"><h2>Filter requests</h2><form className="filter-grid" key={JSON.stringify(filters)} onSubmit={apply}>{["user_id", "key_id", "model_id"].map((id) => <div className="field" key={id}><Label htmlFor={id}>{id.replaceAll("_", " ")}</Label><Input id={id} name={id} defaultValue={filters[id as keyof Filters]} /></div>)}<div className="field"><Label htmlFor="dialect">Protocol</Label><select id="dialect" name="dialect" className="select" defaultValue={filters.dialect ?? ""}><option value="">All</option><option>openai</option><option>anthropic</option><option>gemini</option></select></div><Button>Apply</Button></form></Card><section className="section-block"><h2>{items.length} requests</h2><div className="resource-list">{items.map((item) => <Card className="panel" key={item.id}><div className="resource-row-main"><div><strong>{item.model_id}</strong><small>{item.dialect} · {item.operation} · {item.state} · {new Date(item.started_at).toLocaleString()}</small><code>{item.id}</code></div><span className="status-badge">{item.attempts.length} attempt{item.attempts.length === 1 ? "" : "s"}</span></div>{item.attempts.map((attempt) => <div className="resource-row section-block" key={attempt.id}><div><small>#{attempt.ordinal} · {attempt.connection_id} · {attempt.upstream_model_id} · {attempt.state} · {attempt.usage_status}</small><small>{attempt.input_tokens + attempt.output_tokens} tokens · {attempt.cost_usd === null ? "cost N/A" : `$${attempt.cost_usd}`}</small></div></div>)}</Card>)}</div><div className="row-actions section-block">{filters.cursor && <Button variant="outline" onClick={() => window.history.back()}>Previous page</Button>}{next && <Button variant="outline" onClick={() => navigate({ ...filters, cursor: next })}>Next page</Button>}</div></section></main></AppShell>;
}
