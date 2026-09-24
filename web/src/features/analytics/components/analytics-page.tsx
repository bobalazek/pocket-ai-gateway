"use client";

import Link from "next/link";
import type { ReactNode } from "react";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { AnalyticsBreakdownSection } from "@/features/analytics/components/analytics-breakdowns";
import { CacheTrendChart, FailureTrendChart, UnknownTrendChart, WebSearchTrendChart } from "@/features/analytics/components/analytics-charts";
import { AnalyticsFilters } from "@/features/analytics/components/analytics-filters";
import { useAnalytics } from "@/features/analytics/hooks/use-analytics";
import { dailyPoints } from "@/features/analytics/utils/daily-points";
import { analyticsRequestHref } from "@/features/analytics/utils/request-href";
import { CostTrendChart, RequestTrendChart, TokenTrendChart } from "@/features/usage/components/usage-charts";
import { Metric } from "@/features/usage/components/usage-fields";
import { UsageDailyTable } from "@/features/usage/components/usage-daily-table";

function TrendCard({ title, note, children }: { title: string; note: string; children: ReactNode }) {
  return <Card className="panel chart-panel analytics-chart-card"><h3>{title}</h3><p className="chart-note">{note}</p>{children}</Card>;
}

export default function AnalyticsPage() {
  const model = useAnalytics();
  const points = dailyPoints(model.usage);
  const completed = model.usage.successful_requests + model.usage.failed_requests;
  const failureRate = completed ? `${model.usage.error_rate_percent.toFixed(1)}%` : "—";
  const hasCache = model.usage.cache_creation_input_tokens + model.usage.cache_read_input_tokens > 0;
  const hasSearch = model.usage.web_search_calls > 0;
  const keyRows = model.rankings["key:requests"]?.data ?? [];
  const hasKeyCache = keyRows.some((row) => row.cache_read_input_tokens > 0);
  const hasKeyUnknown = keyRows.some((row) => row.unknown_attempts > 0);

  return <AppShell active="Analytics"><main id="main-content" className="content management-page analytics-page">
    <header className="page-header">
      <div><p className="context">Gateway intelligence</p><h1>Analytics</h1><p className="lede">Trace traffic, known spend, and reliability back to API keys, models, providers, and requests.</p></div>
      <Link className={buttonVariants({ variant: "outline" })} href="/usage/">Limits &amp; pricing</Link>
    </header>
    <AnalyticsFilters model={model} />
    {model.error && <p className="form-error" role="alert">{model.error}</p>}
    {model.loading ? <p className="analytics-loading" role="status">Loading analytics…</p> : model.error ? null : <>
      <nav className="analytics-jump-links" aria-label="Analytics sections">
        <a href="#traffic">Traffic</a><a href="#keys">API keys</a>{model.canManage && <a href="#users">Users</a>}<a href="#models">Models</a><a href="#providers">Providers</a><a href="#operations">Operations</a>
      </nav>
      <section id="traffic" className="analytics-section" aria-label="Traffic overview">
        <div className="analytics-section-heading"><div><h2>Traffic overview</h2><p>{model.usage.from.slice(0, 10)}–{model.usage.to.slice(0, 10)} · UTC daily buckets</p></div><Link href={analyticsRequestHref(model.filters, model.usage)}>View requests</Link></div>
        <div className="metric-grid analytics-kpis">
          <Metric label="Requests" value={model.usage.requests.toLocaleString()} />
          <Metric label="Failed requests" value={model.usage.failed_requests.toLocaleString()} />
          <Metric label="Input + output tokens" value={(model.usage.input_tokens + model.usage.output_tokens).toLocaleString()} />
          <Metric label="Known spend" value={`$${model.usage.known_cost_usd}`} />
          <Metric label="Failure rate" value={failureRate} />
        </div>
        <div className="analytics-chart-grid">
          <TrendCard title="Request volume" note="Distinct gateway requests by day"><RequestTrendChart points={points} /></TrendCard>
          <TrendCard title="Failed requests" note="Final failed gateway requests by day"><FailureTrendChart points={points} /><p className="chart-summary">{model.usage.failed_requests.toLocaleString()} failed of {completed.toLocaleString()} completed requests in this period.</p></TrendCard>
          <TrendCard title="Token volume" note="Provider-reported or reconciled input and output"><TokenTrendChart points={points} /></TrendCard>
          <TrendCard title="Known spend" note="Priced attempts by day · USD"><CostTrendChart points={points} /></TrendCard>
          {hasCache && <TrendCard title="Prompt caching" note="Read and creation tokens; not a cache-hit rate"><CacheTrendChart points={points} /></TrendCard>}
          {model.usage.unknown_attempts > 0 && <TrendCard title="Needs review" note="Attempts with unknown usage or cost"><UnknownTrendChart points={points} /></TrendCard>}
          {hasSearch && <TrendCard title="Hosted web search" note="Provider-reported call counts"><WebSearchTrendChart points={points} /></TrendCard>}
        </div>
        <details className="analytics-table-disclosure"><summary>Daily data table</summary><UsageDailyTable usage={model.usage} points={points} /></details>
        <p className="analytics-caveat">Requests and gateway duration use request start time; tokens and spend use attempt start time. Spend reflects recorded prices and later restatements, not a provider invoice. Unknown attempts remain separate.</p>
      </section>

      <div id="keys"><AnalyticsBreakdownSection model={model} dimension="key" title="API keys" description="Compare workload and cost by the key that made each request. Select a key in the table to redraw every chart for it." charts={[
        { title: "Requests by API key", note: "Top keys by distinct request count", metric: "requests", sort: "requests" },
        { title: "Known spend by API key", note: "Top keys by priced spend · USD", metric: "spend", sort: "known_cost" },
        { title: "Tokens by API key", note: "Input plus output tokens", metric: "tokens", sort: "tokens" },
        { title: "Gateway p95 by API key", note: "End-to-end request duration; finished requests only", metric: "p95", sort: "p95_latency" },
        ...(hasKeyCache ? [{ title: "Cache reads by API key", note: "Provider-reported prompt-cache read tokens", metric: "cache_reads" as const, sort: "requests" as const }] : []),
        ...(hasKeyUnknown ? [{ title: "Needs review by API key", note: "Attempts with unknown usage or cost among the busiest keys", metric: "unknown" as const, sort: "requests" as const }] : []),
      ]} /></div>
      {model.canManage && <div id="users"><AnalyticsBreakdownSection model={model} dimension="user" title="Users" description="Attributed to the account that owns each API key." charts={[
        { title: "Requests by user", note: "Top users by distinct request count", metric: "requests", sort: "requests" },
        { title: "Known spend by user", note: "Top users by priced spend · USD", metric: "spend", sort: "known_cost" },
        { title: "Failures by user", note: "Final failed gateway requests among the busiest users", metric: "failures", sort: "requests" },
      ]} /></div>}
      <div id="models"><AnalyticsBreakdownSection model={model} dimension="model" title="Models" description="Public model names attributed to gateway requests." charts={[
        { title: "Requests by model", note: "Top public models by request count", metric: "requests", sort: "requests" },
        { title: "Known spend by model", note: "Top public models by priced spend · USD", metric: "spend", sort: "known_cost" },
        { title: "Gateway p95 by model", note: "End-to-end request duration; finished requests only", metric: "p95", sort: "p95_latency" },
        { title: "Failures by model", note: "Among the busiest models by request count", metric: "failures", sort: "requests" },
      ]} /></div>
      <div id="providers"><AnalyticsBreakdownSection model={model} dimension="connection" title="Providers" description="A request can touch more than one connection during fallback; provider rows may overlap." charts={[
        { title: "Requests touching provider", note: "Distinct requests per connection", metric: "requests", sort: "requests" },
        { title: "Known spend by provider", note: "Priced attempts per connection · USD", metric: "spend", sort: "known_cost" },
        { title: "Gateway p95 by provider", note: "Whole-request duration, including routing and fallback", metric: "p95", sort: "p95_latency" },
        { title: "Failed attempts by provider", note: "Upstream failures among the busiest connections, including successful fallback requests", metric: "failed_attempts", sort: "requests" },
      ]} /></div>
      <div id="operations" className="analytics-operation-grid">
        <AnalyticsBreakdownSection model={model} dimension="dialect" title="Client protocols" description="Which client API families generated traffic." charts={[{ title: "Requests by protocol", note: "OpenAI, Anthropic, Gemini, and other recorded dialects", metric: "requests", sort: "requests" }]} />
        <AnalyticsBreakdownSection model={model} dimension="operation" title="Operations" description="The gateway operations clients invoked." charts={[{ title: "Requests by operation", note: "Text, media, and other operation types", metric: "requests", sort: "requests" }]} />
        <AnalyticsBreakdownSection model={model} dimension="state" title="Outcomes" description="Request states, including unsuccessful and interrupted work." charts={[{ title: "Requests by outcome", note: "Observed gateway request states", metric: "requests", sort: "requests" }]} />
      </div>
    </>}
  </main></AppShell>;
}
