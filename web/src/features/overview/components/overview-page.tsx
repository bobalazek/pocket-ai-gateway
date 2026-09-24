"use client";

import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useOverview } from "@/features/overview/hooks/use-overview";
import { RuntimeHealth } from "@/features/status/components/runtime-health";
import { RequestTrendChart } from "@/features/usage/components/usage-charts";

export default function OverviewPage() {
  const { data, loading } = useOverview();
  const usage = data.usage;
  const setupIncomplete = data.setup && Object.values(data.setup).some((complete) => !complete);
  const recentPoints = usage?.points.slice(-7) ?? [];
  const period = usage
    ? `${new Date(usage.from).toLocaleDateString()}–${new Date(usage.to).toLocaleDateString()}`
    : "Current period";

  return <AppShell active="Overview">
    <main id="main-content" className="content overview-page">
      <header className="page-header">
        <div><h1>Overview</h1><p className="lede">Gateway activity, spend, and health at a glance.</p></div>
        <Link className={buttonVariants({ variant: "outline" })} href="/usage/">Explore usage</Link>
      </header>

      {data.partial && <p className="form-error" role="status">Some dashboard data is unavailable. Available values remain current.</p>}
      <section className="overview-status" aria-label="Gateway status">
        <RuntimeHealth compact />
        <Link href="/status/">View status</Link>
      </section>

      <section className="overview-section" aria-labelledby="overview-activity-title">
        <div className="section-heading"><h2 id="overview-activity-title">Activity</h2><span className="section-period">{loading ? "Loading period…" : period}</span></div>
        <div className="metric-grid overview-metrics">
          <Metric label="Requests" value={loading ? "…" : usage?.requests.toLocaleString() ?? "—"} note="in period" />
          <Metric label="Tokens" value={loading ? "…" : usage ? (usage.input_tokens + usage.output_tokens).toLocaleString() : "—"} note="input + output" />
          <Metric label="Known spend" value={loading ? "…" : usage ? money(usage.known_cost_usd) : "—"} note="priced attempts" />
          <Metric label="Needs review" value={loading ? "…" : usage?.unknown_attempts.toLocaleString() ?? "—"} note="unknown usage" />
        </div>
      </section>

      <div className="overview-main">
        <Card className="panel overview-chart">
          <div className="section-heading"><div><h2>Request volume</h2><p className="chart-note">Daily requests · latest seven days</p></div></div>
          {recentPoints.length ? <RequestTrendChart points={recentPoints} /> : <p className="empty-copy">No request activity in this period.</p>}
          <p className="chart-summary">Total in period: {loading ? "…" : usage?.requests.toLocaleString() ?? "unavailable"} requests.</p>
        </Card>
        <Card className="panel overview-recent">
          <div className="section-heading"><h2>Recent requests</h2><Link className="text-link" href="/requests/">View all</Link></div>
          {data.recentRequests.length ? <ul className="activity-list">
            {data.recentRequests.map((request) => <li key={request.id}>
              <Link href={`/requests/?request_id=${encodeURIComponent(request.id)}`}>
                <span><strong>{request.model_id}</strong><small>{request.operation} · {new Date(request.started_at).toLocaleString()}</small></span>
                <span className="request-state" data-state={request.state}>{request.state}</span>
              </Link>
            </li>)}
          </ul> : !loading && <p className="empty-copy">{data.recentRequestsAvailable ? "No request activity yet." : "Recent request activity is unavailable."}</p>}
        </Card>
      </div>

      {setupIncomplete && data.setup && <section className="next-step" aria-labelledby="next-step-title">
        <div><h2 id="next-step-title">Complete gateway setup</h2><p>{!data.setup.providers ? "Connect a provider first." : !data.setup.models ? "Publish a model route next." : "Issue an API key for your application."}</p></div>
        <Link className={buttonVariants()} href={!data.setup.providers ? "/providers/" : !data.setup.models ? "/models/" : "/keys/"}>Continue setup</Link>
      </section>}
      {data.diagnosticsVersion && <p className="fine-print">Pocket AI Gateway {data.diagnosticsVersion}</p>}
    </main>
  </AppShell>;
}

function Metric({ label, value, note }: { label: string; value: string; note: string }) {
  return <Card className="overview-metric"><small>{label}</small><strong>{value}</strong><span>{note}</span></Card>;
}

function money(value: string) {
  return `$${Number(value).toFixed(4)}`;
}
