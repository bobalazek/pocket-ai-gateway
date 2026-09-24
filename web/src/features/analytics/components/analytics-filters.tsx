import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field } from "@/features/usage/components/usage-fields";
import type { useAnalytics } from "@/features/analytics/hooks/use-analytics";
import { localDatetime } from "@/features/usage/utils/usage.utils";

type AnalyticsModel = ReturnType<typeof useAnalytics>;

export function AnalyticsFilters({ model }: { model: AnalyticsModel }) {
  const selected = [
    ["user_id", "User", model.filters.user_id],
    ["key_id", "API key", model.filters.key_id],
    ["model_id", "Model", model.filters.model_id],
    ["connection_id", "Provider", model.filters.connection_id],
  ] as const;
  return <Card className="panel analytics-filters">
    <div className="analytics-toolbar-row">
      <div><strong>Time range</strong><p className="chart-note">All charts use the same server-side filters.</p></div>
      <div className="analytics-periods">
        {[7, 30, 90].map((days) => <Button key={days} type="button" variant={model.preset === days ? "default" : "outline"} aria-pressed={model.preset === days} onClick={() => model.setPeriod(days)}>Last {days} days</Button>)}
        <Button type="button" variant="outline" onClick={model.refresh}>Refresh</Button>
      </div>
    </div>
    <details className="analytics-custom-range">
      <summary>Custom range{model.canManage ? " and user" : ""}</summary>
      <form className="filter-grid" key={JSON.stringify(model.filters)} onSubmit={model.applyCustomRange}>
        <Field id="from" label="From" type="datetime-local" defaultValue={localDatetime(model.filters.from)} />
        <Field id="to" label="To" type="datetime-local" defaultValue={localDatetime(model.filters.to)} />
        {model.canManage && <Field id="user_id" label="User ID" defaultValue={model.filters.user_id} />}
        <Button>Apply range</Button>
      </form>
    </details>
    {selected.some(([, , value]) => value) && <div className="analytics-filter-chips" aria-label="Active filters">
      {selected.filter(([, , value]) => value).map(([kind, label, value]) => <button type="button" key={kind} onClick={() => model.clearDimension(kind)} aria-label={`Remove ${label} filter`}>
        {label}: <strong>{value}</strong><span aria-hidden="true">×</span>
      </button>)}
    </div>}
  </Card>;
}
