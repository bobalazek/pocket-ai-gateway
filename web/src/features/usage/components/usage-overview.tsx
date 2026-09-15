import { Area, AreaChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ChartContainer } from "@/components/ui/chart";
import type { useUsage } from "@/features/usage/hooks/use-usage";
import { localDatetime } from "@/features/usage/utils/usage.utils";
import { Field, Metric } from "@/features/usage/components/usage-fields";

type UsageModel = ReturnType<typeof useUsage>;

export function UsageOverview({ model }: { model: UsageModel }) {
  const hasCacheUsage = model.usage.cache_creation_input_tokens > 0 || model.usage.cache_read_input_tokens > 0 || model.usage.cache_creation_5m_input_tokens > 0 || model.usage.cache_creation_1h_input_tokens > 0;
  const hasWebSearchUsage = model.usage.web_search_calls > 0;

  return (
    <>
      <Card className="panel">
        <h2>Filter usage</h2>
        <form className="filter-grid" key={JSON.stringify(model.filters)} onSubmit={model.applyFilters}>
          <Field id="from" label="From" type="datetime-local" defaultValue={localDatetime(model.filters.from)} />
          <Field id="to" label="To" type="datetime-local" defaultValue={localDatetime(model.filters.to)} />
          {model.canManage && <Field id="user_id" label="User ID" defaultValue={model.filters.user_id} />}
          <Field id="key_id" label="Key ID" defaultValue={model.filters.key_id} />
          <Field id="model_id" label="Model ID" defaultValue={model.filters.model_id} />
          <Field id="connection_id" label="Connection ID" defaultValue={model.filters.connection_id} />
          <Button>Apply</Button>
        </form>
      </Card>
      <section className="metric-grid" aria-label="Usage totals">
        <Metric label="Requests" value={model.usage.requests.toLocaleString()} />
        <Metric label="Tokens" value={(model.usage.input_tokens + model.usage.output_tokens).toLocaleString()} />
        <Metric label="Current cost" value={`$${model.usage.known_cost_usd}`} />
        <Metric label="Needs review" value={model.usage.unknown_attempts.toLocaleString()} />
      </section>
      {hasCacheUsage && (
        <Card className="panel">
          <p className="context">Prompt cache usage</p>
          <div className="metric-grid">
            <Metric label="Cache writes" value={model.usage.cache_creation_input_tokens.toLocaleString()} />
            <Metric label="Cache reads" value={model.usage.cache_read_input_tokens.toLocaleString()} />
            <Metric label="5-minute writes" value={model.usage.cache_creation_5m_input_tokens.toLocaleString()} />
            <Metric label="1-hour writes" value={model.usage.cache_creation_1h_input_tokens.toLocaleString()} />
          </div>
        </Card>
      )}
      {hasWebSearchUsage && (
        <Card className="panel">
          <p className="context">Hosted web search</p>
          <div className="metric-grid">
            <Metric label="Web-search calls" value={model.usage.web_search_calls.toLocaleString()} />
          </div>
          <p className="help-text">Token usage is included above. Search-call cost remains unknown because the gateway has no provider search price contract.</p>
        </Card>
      )}
      <Card className="panel">
        <p className="context">Cost provenance</p>
        <div className="metric-grid">
          <Metric label="Estimated" value={`$${model.usage.estimated_cost_usd}`} />
          <Metric label="As recorded" value={`$${model.usage.as_recorded_cost_usd}`} />
          <Metric label="Restatement change" value={`$${model.usage.restatement_delta_usd}`} />
        </div>
      </Card>
      <Card className="panel chart-panel">
        <h2>Tokens by day</h2>
        {model.chartData.length ? (
          <>
            <ChartContainer label="Daily token usage"><AreaChart responsive data={model.chartData}><CartesianGrid vertical={false} /><XAxis dataKey="date" /><YAxis width={52} /><Tooltip /><Area dataKey="tokens" stroke="var(--accent)" fill="var(--accent-soft)" /></AreaChart></ChartContainer>
            <div className="table-wrap" tabIndex={0} role="region" aria-label="Scrollable daily usage table">
              <table>
                <thead>
                  <tr>
                    <th>Date</th><th>Requests</th><th>Input</th><th>Output</th>
                    {hasCacheUsage && <><th>Cache writes</th><th>Cache reads</th><th>5m writes</th><th>1h writes</th></>}
                    {hasWebSearchUsage && <th>Web search</th>}
                    <th>Cost</th><th>Unknown</th>
                  </tr>
                </thead>
                <tbody>
                  {model.usage.points.map((point) => (
                    <tr key={point.date}>
                      <td>{point.date}</td><td>{point.requests}</td><td>{point.input_tokens}</td><td>{point.output_tokens}</td>
                      {hasCacheUsage && <><td>{point.cache_creation_input_tokens}</td><td>{point.cache_read_input_tokens}</td><td>{point.cache_creation_5m_input_tokens}</td><td>{point.cache_creation_1h_input_tokens}</td></>}
                      {hasWebSearchUsage && <td>{point.web_search_calls}</td>}
                      <td>${point.known_cost_usd}</td><td>{point.unknown_attempts}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : <p className="empty-copy">No settled usage in this period.</p>}
      </Card>
    </>
  );
}
