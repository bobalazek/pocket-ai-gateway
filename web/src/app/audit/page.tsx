"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type AuditEvent, type AuditFilters } from "@/lib/api-client";

const keys = ["actor_user_id", "action", "resource_type", "from", "to", "cursor"] as const;
function readFilters() {
  const query = new URLSearchParams(window.location.search); const result: AuditFilters = {}; let changed = false;
  for (const key of keys) {
    let value = query.get(key) ?? "";
    if ((key === "from" || key === "to") && value) { const canonical = canonicalTime(value); if (!canonical) { query.delete(key); changed = true; value = ""; } else if (canonical !== value) { query.set(key, canonical); changed = true; value = canonical; } }
    if (value) result[key] = value;
  }
  if (changed) window.history.replaceState(window.history.state, "", query.size ? `?${query}` : window.location.pathname);
  return result;
}

export default function AuditPage() {
  const active = useRef<AbortController | null>(null);
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [filters, setFilters] = useState<AuditFilters>({});
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
	const [depth, setDepth] = useState(0);

  async function load(query: AuditFilters) {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setBusy(true);
		setItems([]); setNext("");
    try {
      const result = await gatewayAPI.audit(query, controller.signal);
      if (controller.signal.aborted) return;
      setItems(result.data ?? []); setNext(result.next_cursor); setError("");
    } catch (failure) {
      if (!controller.signal.aborted) setError(failure instanceof GatewayAPIError ? failure.message : "Audit events are unavailable");
    } finally { if (!controller.signal.aborted) setBusy(false); }
  }

  useEffect(() => { const sync = () => { const query = readFilters(); const pageDepth = Number(window.history.state?.auditDepth ?? 0); setDepth(pageDepth); setFilters(query); void load(query); }; window.history.replaceState({ ...window.history.state, auditDepth: Number(window.history.state?.auditDepth ?? 0) }, ""); sync(); window.addEventListener("popstate", sync); return () => { active.current?.abort(); window.removeEventListener("popstate", sync); }; }, []);
  function navigate(query: AuditFilters) { const values = new URLSearchParams(); for (const [key, value] of Object.entries(query)) if (value) values.set(key, value); const nextDepth = depth + 1; window.history.pushState({ ...window.history.state, auditDepth: nextDepth }, "", values.size ? `?${values}` : window.location.pathname); setDepth(nextDepth); setFilters(query); void load(query); }
  function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); navigate({ actor_user_id: text(form, "actor_user_id"), action: text(form, "action"), resource_type: text(form, "resource_type"), from: isoTime(text(form, "from")), to: isoTime(text(form, "to")) }); }

  return <AppShell active="Audit"><main id="main-content" className="content management-page">
    <header className="page-header"><div><h1>Audit log</h1><p className="lede">Local administrative activity. Sensitive values and request content are not recorded.</p></div></header>
    <Card className="panel compact-panel"><form className="filter-grid" key={JSON.stringify(filters)} onSubmit={submit}>
      <Field name="actor_user_id" label="Actor ID" defaultValue={filters.actor_user_id} />
      <Field name="action" label="Action" placeholder="provider.update" defaultValue={filters.action} />
      <Field name="resource_type" label="Resource type" placeholder="provider_connection" defaultValue={filters.resource_type} />
      <Field name="from" label="From" type="datetime-local" defaultValue={localTime(filters.from)} />
      <Field name="to" label="To" type="datetime-local" defaultValue={localTime(filters.to)} />
      <Button disabled={busy}>Apply filters</Button>
    </form></Card>
    {error && <p className="form-error" role="alert">{error}</p>}
    {items.length ? <div className="resource-list">{items.map((item) => <Card className="resource-row stack" key={item.id}><div><strong>{item.action.replaceAll(".", " ")}</strong><small>{item.resource_type} · {item.resource_id} · {new Date(item.created_at).toLocaleString()}</small><code>{item.actor_user_id || "system"}</code>{item.detail_json && item.detail_json !== "{}" && <details className="audit-detail"><summary>Details</summary><pre className="code-block">{prettyJSON(item.detail_json)}</pre></details>}</div></Card>)}</div> : !error && !busy && <p className="empty-copy">No audit events match these filters.</p>}
    <div className="row-actions section-block">{filters.cursor && depth > 0 && <Button type="button" variant="outline" disabled={busy} onClick={() => window.history.back()}>Previous page</Button>}{next && <Button type="button" variant="outline" disabled={busy} onClick={() => navigate({ ...filters, cursor: next })}>Next page</Button>}</div>
  </main></AppShell>;
}

function Field({ name, label, type = "text", placeholder, defaultValue }: { name: string; label: string; type?: string; placeholder?: string; defaultValue?: string }) { return <div className="field"><Label htmlFor={name}>{label}</Label><Input id={name} name={name} type={type} placeholder={placeholder} defaultValue={defaultValue} /></div>; }
function text(form: FormData, name: string) { return String(form.get(name) ?? "").trim(); }
function isoTime(value: string) { return value ? new Date(value).toISOString() : ""; }
function canonicalTime(value: string) { const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-](\d{2}):(\d{2}))$/.exec(value); if (!match) return ""; const [year, month, day, hour, minute, second, offsetHour, offsetMinute] = [match[1], match[2], match[3], match[4], match[5], match[6], match[8] ?? "0", match[9] ?? "0"].map(Number); if (month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate() || hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59) return ""; const date = new Date(value); return Number.isNaN(date.getTime()) ? "" : date.toISOString(); }
function localTime(value?: string) { if (!value) return ""; const date = new Date(value); return Number.isNaN(date.getTime()) ? "" : new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); }
function prettyJSON(value: string) { try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; } }
