import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { CostTrendChart, TokenTrendChart } from "@/features/usage/components/usage-charts";
import { UsageDailyTable } from "@/features/usage/components/usage-daily-table";
import { Field, Metric } from "@/features/usage/components/usage-fields";
import type { useUsage } from "@/features/usage/hooks/use-usage";
import { localDatetime } from "@/features/usage/utils/usage.utils";

type UsageModel = ReturnType<typeof useUsage>;

export function UsageOverview({ model }: { model: UsageModel }) {
  const hasCacheUsage = model.usage.cache_creation_input_tokens > 0 || model.usage.cache_read_input_tokens > 0 || model.usage.cache_creation_5m_input_tokens > 0 || model.usage.cache_creation_1h_input_tokens > 0;
  const hasWebSearchUsage = model.usage.web_search_calls > 0;
  const points = model.usage.points;

  return <>
    <section className="metric-grid" aria-label="Usage totals">
      <Metric label="Requests" value={model.usage.requests.toLocaleString()} />
      <Metric label="Tokens" value={(model.usage.input_tokens + model.usage.output_tokens).toLocaleString()} />
      <Metric label="Known spend" value={`$${model.usage.known_cost_usd}`} />
      <Metric label="Needs review" value={model.usage.unknown_attempts.toLocaleString()} />
    </section>

    <section className="usage-chart-grid" aria-label="Usage trends">
      <Card className="panel chart-panel">
        <div className="section-heading"><div><h2>Token volume</h2><p className="chart-note">Input {model.usage.input_tokens.toLocaleString()} · Output {model.usage.output_tokens.toLocaleString()}</p></div></div>
        {points.length ? <TokenTrendChart points={points} /> : <p className="empty-copy">No settled usage in this period.</p>}
      </Card>
      <Card className="panel chart-panel">
        <div className="section-heading"><div><h2>Known spend</h2><p className="chart-note">USD by day · priced attempts only</p></div></div>
        {points.length ? <CostTrendChart points={points} /> : <p className="empty-copy">No priced usage in this period.</p>}
      </Card>
    </section>

    <details className="filter-disclosure">
      <summary>Filter usage <span>Time, user, key, model, or provider</span></summary>
      <form className="filter-grid" key={JSON.stringify(model.filters)} onSubmit={model.applyFilters}>
        <Field id="from" label="From" type="datetime-local" defaultValue={localDatetime(model.filters.from)} />
        <Field id="to" label="To" type="datetime-local" defaultValue={localDatetime(model.filters.to)} />
        {model.canManage && <Field id="user_id" label="User ID" defaultValue={model.filters.user_id} />}
        <Field id="key_id" label="Key ID" defaultValue={model.filters.key_id} />
        <Field id="model_id" label="Model ID" defaultValue={model.filters.model_id} />
        <Field id="connection_id" label="Connection ID" defaultValue={model.filters.connection_id} />
        <Button>Apply filters</Button>
      </form>
    </details>

    <Card className="panel section-block">
      <div className="section-heading"><div><h2>Daily breakdown</h2><p className="chart-note">Detailed counts behind the charts</p></div></div>
      <UsageDailyTable usage={model.usage} points={points} />
    </Card>

    <section className="analytics-detail section-block" aria-labelledby="cost-provenance-title">
      <h2 id="cost-provenance-title">Cost provenance</h2>
      <div className="metric-grid">
        <Metric label="Estimated" value={`$${model.usage.estimated_cost_usd}`} />
        <Metric label="As recorded" value={`$${model.usage.as_recorded_cost_usd}`} />
        <Metric label="Restatement change" value={`$${model.usage.restatement_delta_usd}`} />
      </div>
    </section>
    {hasCacheUsage && <section className="analytics-detail section-block" aria-labelledby="cache-title">
      <h2 id="cache-title">Prompt cache usage</h2>
      <div className="metric-grid">
        <Metric label="Cache writes" value={model.usage.cache_creation_input_tokens.toLocaleString()} />
        <Metric label="Cache reads" value={model.usage.cache_read_input_tokens.toLocaleString()} />
        <Metric label="5-minute writes" value={model.usage.cache_creation_5m_input_tokens.toLocaleString()} />
        <Metric label="1-hour writes" value={model.usage.cache_creation_1h_input_tokens.toLocaleString()} />
      </div>
    </section>}
    {hasWebSearchUsage && <section className="analytics-detail section-block" aria-labelledby="search-title">
      <h2 id="search-title">Hosted web search</h2>
      <div className="metric-grid"><Metric label="Web-search calls" value={model.usage.web_search_calls.toLocaleString()} /></div>
      <p className="help-text">Token usage is included above. Search-call cost remains unknown because the gateway has no provider search price contract.</p>
    </section>}
  </>;
}
