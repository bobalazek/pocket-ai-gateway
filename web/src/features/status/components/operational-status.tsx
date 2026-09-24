"use client";

import { useGatewayUser } from "@/components/setup-gate";
import { Button } from "@/components/ui/button";
import { useOperationalStatus } from "@/features/status/hooks/use-operational-status";

const names = { system_database: "System database", data_database: "History database", usage_projection: "Usage projection" } as const;

export function OperationalStatus() {
  const user = useGatewayUser();
  const admin = user?.role === "owner" || user?.role === "admin";
  const { status, error, loading, refresh } = useOperationalStatus(admin);
  if (!admin) return <p className="fine-print">An administrator can inspect storage and accounting checks after signing in.</p>;

  return <section className="operational-status" aria-labelledby="operational-status-title">
    <div className="section-heading"><div><p className="context">Private diagnostics</p><h2 id="operational-status-title">Operational checks</h2></div><Button type="button" variant="outline" disabled={loading} onClick={refresh}>Refresh checks</Button></div>
    <p className="fine-print">These checks cover the local databases and accounting backlog. They do not test provider credentials or model availability.</p>
    {loading && !status && <p role="status">Checking storage and accounting…</p>}
    {error && <p role="alert">Could not refresh component checks. Try again.</p>}
    {status && <><div className="status-list" aria-label="Component checks">{status.checks.map((check) => <div key={check.id}><span>{names[check.id]}</span><strong data-state={check.state}>{check.state === "ready" ? "Ready" : check.id === "usage_projection" && status.outbox?.full ? "At capacity" : "Unavailable"}</strong></div>)}</div>
      {status.outbox && <p className="fine-print">{status.outbox.pending_events.toLocaleString()} pending events · {status.outbox.reserved_events.toLocaleString()} active reservations{status.outbox.oldest_event_at ? ` · oldest event ${new Date(status.outbox.oldest_event_at).toLocaleString()}` : ""}</p>}
      <p className="fine-print">Checked {new Date(status.checked_at).toLocaleString()} · <code>GET /api/v1/admin/status</code></p></>}
  </section>;
}
