"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAudit } from "@/features/audit/hooks/use-audit";
import { localTime, prettyJSON } from "@/features/audit/utils/audit.utils";

export default function AuditPage() {
  const { items, filters, next, busy, error, depth, navigate, submit } = useAudit();

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
