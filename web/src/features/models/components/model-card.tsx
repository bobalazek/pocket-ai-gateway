import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { routeStrategies } from "@/features/models/components/model-fields";
import type { useModels } from "@/features/models/hooks/use-models";
import type { CatalogModel, PublicModel } from "@/features/models/types/models.types";

type ModelsModel = ReturnType<typeof useModels>;

export function ModelCard({ item, dashboard }: { item: CatalogModel | PublicModel; dashboard: ModelsModel }) {
  const model = item as PublicModel;
  const configured = dashboard.routes[item.id] ?? [];
  const preview = dashboard.previews[item.id];
  const strategy = dashboard.selectedStrategies[item.id] ?? model.routing_strategy ?? "fixed";
  const maximumTargets = model.routing_policy?.max_targets_by_strategy[strategy] ?? 1;

  return (
    <Card className="panel">
      <div className="resource-row-main"><div><strong>{item.label}</strong><code>{item.id}</code><small>{item.adapter} · {item.capabilities.join(", ")}{dashboard.manager ? ` · ${model.routing_strategy.replaceAll("_", " ")}` : ""}</small></div></div>
      {dashboard.manager && (
        <form className="route-preview-controls" onSubmit={(event) => dashboard.preview(event, model)}>
          <div className="field"><Label htmlFor={`preview-operation-${model.id}`}>Operation</Label><Input id={`preview-operation-${model.id}`} name="operation" defaultValue="chat/completions" required /></div>
          <div className="field"><Label htmlFor={`preview-input-${model.id}`}>Input tokens</Label><Input id={`preview-input-${model.id}`} name="estimated_input_tokens" type="number" min="0" defaultValue="1000" required /></div>
          <div className="field"><Label htmlFor={`preview-output-${model.id}`}>Output tokens</Label><Input id={`preview-output-${model.id}`} name="estimated_output_tokens" type="number" min="0" defaultValue="500" required /></div>
          <label className="checkbox-row"><input name="streaming" type="checkbox" /> Streaming</label>
          <Button variant="outline" disabled={dashboard.busy}>Preview route</Button>
        </form>
      )}
      {dashboard.manager && (
        <details className="grant-editor">
          <summary>Routing strategy and targets</summary>
          <form onSubmit={(event) => dashboard.saveRoute(event, model)}>
            <div className="inline-fields">
              <div className="field"><Label htmlFor={`strategy-${model.id}`}>Strategy</Label><select id={`strategy-${model.id}`} name="strategy" className="select" value={strategy} onChange={(event) => dashboard.selectStrategy(model.id, event.target.value as typeof strategy)}>{routeStrategies.filter((option) => model.routing_policy.allowed_strategies.includes(option.value)).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></div>
              {model.routing_policy.free_only_allowed && <label className="checkbox-row"><input name="free_only" type="checkbox" defaultChecked={model.free_only} /> Require verified zero provider pricing</label>}
            </div>
            <div className="resource-list">
              {dashboard.targets.map((target, index) => {
                const current = configured.find((entry) => entry.upstream_model_id === target.id);
                return <div className="route-target" key={`${target.id}:${strategy}`}><label className="checkbox-row"><input name="target" value={target.id} type={maximumTargets === 1 ? "radio" : "checkbox"} defaultChecked={maximumTargets === 1 ? target.id === model.target_model_id : Boolean(current)} /> {target.upstream_id}</label><Input aria-label={`${target.upstream_id} priority`} name={`priority:${target.id}`} type="number" min="1" max="1000" defaultValue={String(current?.priority ?? index + 1)} /><Input aria-label={`${target.upstream_id} weight`} name={`weight:${target.id}`} type="number" min="1" max="10000" defaultValue={String(current?.weight ?? 1)} /></div>;
              })}
            </div>
            <Button disabled={dashboard.busy}>Save route</Button>
          </form>
        </details>
      )}
      {preview && <div className="route-preview" role="status"><strong>Selected: {preview.route.targets[0]?.upstream_id ?? "none"}</strong><small>{preview.route.selection_reason}</small><small>{preview.input.operation} · {preview.input.streaming ? "streaming" : "non-streaming"} · {preview.input.estimated_input_tokens} input / {preview.input.estimated_output_tokens} output tokens</small>{preview.route.rejected.map((entry) => <small key={`${entry.connection_id}:${entry.upstream_model_id}`}>{entry.upstream_model_id}: {entry.reason.replaceAll("_", " ")}</small>)}</div>}
    </Card>
  );
}
