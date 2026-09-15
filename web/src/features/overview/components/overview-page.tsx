"use client";

import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useOverview } from "@/features/overview/hooks/use-overview";
import { RuntimeHealth } from "@/features/status/components/runtime-health";

export default function OverviewPage() {
  const { data, loading } = useOverview();
  const recentFailures = data.recentRequests.filter((request) => request.state === "failed").length;
  const setupIncomplete = data.setup && Object.values(data.setup).some((complete) => !complete);
  const period = data.usage
    ? `${new Date(data.usage.from).toLocaleString()} to ${new Date(data.usage.to).toLocaleString()}`
    : "the current usage period";

  return (
    <AppShell active="Overview">
      <main id="main-content" className="content">
        <header className="page-header">
          <div>
            <p className="context">Dashboard</p>
            <h1>Your gateway control center.</h1>
            <p className="lede">Manage access, connect providers, publish stable models, and inspect local usage.</p>
          </div>
          <a className={buttonVariants()} href="/readyz">Check readiness</a>
        </header>
        {data.partial && <p className="form-error" role="status">Some dashboard data is unavailable. The available values below are still current.</p>}
        <RuntimeHealth />
        <section className="section-block" aria-labelledby="activity-title">
          <div className="section-heading">
            <div><p className="context">Usage period</p><h2 id="activity-title">Gateway activity</h2></div>
            <small>{loading ? "Loading period…" : period}</small>
          </div>
          <div className="metric-grid">
            <Metric label="Requests in period" value={loading ? "…" : data.usage?.requests.toLocaleString() ?? "Unavailable"} />
            <Metric label="Failures in latest 5" value={loading ? "…" : data.recentRequestsAvailable ? recentFailures.toLocaleString() : "Unavailable"} />
            <Metric label="Known spend" value={loading ? "…" : data.usage ? `$${data.usage.known_cost_usd}` : "Unavailable"} />
            <Metric label="Estimated spend" value={loading ? "…" : data.usage ? `$${data.usage.estimated_cost_usd}` : "Unavailable"} />
            <Metric label="Attempts needing review" value={loading ? "…" : data.usage?.unknown_attempts.toLocaleString() ?? "Unavailable"} />
          </div>
        </section>
        <Card className="panel section-block">
          <div className="section-heading">
            <div><p className="context">Recent activity</p><h2>Latest requests</h2></div>
            <Link className="text-link" href="/requests/">View all</Link>
          </div>
          {data.recentRequests.length ? (
            <div className="resource-list">
              {data.recentRequests.map((request) => (
                <div className="resource-row" key={request.id}>
                  <div><strong>{request.model_id}</strong><small>{request.operation} · {request.state} · {new Date(request.started_at).toLocaleString()}</small></div>
                </div>
              ))}
            </div>
          ) : !loading && (data.recentRequestsAvailable ? <p className="empty-copy">No request activity yet.</p> : <p className="empty-copy">Recent request activity is unavailable.</p>)}
        </Card>
        {setupIncomplete && data.setup && (
          <section className="next-step" aria-labelledby="next-step-title">
            <div>
              <h2 id="next-step-title">Complete gateway setup</h2>
              <p>{!data.setup.providers ? "Connect a provider first." : !data.setup.models ? "Publish a model route next." : "Issue an API key for your application."}</p>
            </div>
            <Link className={buttonVariants()} href={!data.setup.providers ? "/providers/" : !data.setup.models ? "/models/" : "/keys/"}>Continue setup</Link>
          </section>
        )}
        {data.diagnosticsVersion && <p className="fine-print">Pocket AI Gateway {data.diagnosticsVersion}</p>}
      </main>
    </AppShell>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <Card><small>{label}</small><strong>{value}</strong></Card>;
}
