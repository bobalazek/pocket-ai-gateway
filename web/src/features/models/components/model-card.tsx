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
  const embeddingModel = item.capabilities.includes("embeddings");
  const opaqueMediaModel = ["images", "image_edit", "image_variation", "audio_speech", "audio_transcription", "audio_translation"].some((capability) => item.capabilities.includes(capability));
  const promptCacheModel = item.capabilities.includes("prompt_cache");
  const webSearchModel = item.capabilities.includes("web_search");
  const preview = dashboard.previews[item.id];
  const routingHelp = webSearchModel
    ? item.adapter === "anthropic"
      ? "Anthropic basic web search requires chat and web_search on the public and upstream model plus an Anthropic-adapter target using the built-in Anthropic preset. Direct JSON or SSE web_search_20250305 is supported with a 1–4 use limit and no prompt-cache controls. Requests never fall back after dispatch and reject lowest-cost, free-only, and spend policies because provider search cost is not represented by the gateway."
      : "Web-search Responses require chat and web_search on the public and upstream model plus an OpenAI-adapter target using the built-in OpenAI preset. They never fall back after dispatch and reject lowest-cost, free-only, and spend policies because provider search cost is not available to the gateway."
    : embeddingModel
      ? "Embedding models keep one fixed target so their vector space cannot change."
    : opaqueMediaModel
      ? "Image and audio requests select one target without post-dispatch fallback. Active token, output-token, spend, free-only, and lowest-cost policies block these operations."
      : promptCacheModel
        ? "Ordinary requests use the configured routing strategy. Prompt-cache requests reject lowest-cost and free-only routes and do not fall back after dispatch because cache pricing is not yet represented."
        : "Eligibility and grants are checked before scoring. Priority is also the fallback order.";

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
              <div className="field"><Label htmlFor={`strategy-${model.id}`}>Strategy</Label><select id={`strategy-${model.id}`} name="strategy" className="select" defaultValue={model.routing_strategy}>{routeStrategies.filter((strategy) => !embeddingModel || strategy.value === "fixed").map((strategy) => <option key={strategy.value} value={strategy.value}>{strategy.label}</option>)}</select></div>
              <label className="checkbox-row"><input name="free_only" type="checkbox" defaultChecked={model.free_only} /> Require verified zero provider pricing</label>
            </div>
            <div className="resource-list">
              {dashboard.targets.map((target, index) => {
                const current = configured.find((entry) => entry.upstream_model_id === target.id);
                return <div className="route-target" key={target.id}><label className="checkbox-row"><input name={`target:${target.id}`} type="checkbox" defaultChecked={Boolean(current)} disabled={embeddingModel && !current} /> {target.upstream_id}</label><Input aria-label={`${target.upstream_id} priority`} name={`priority:${target.id}`} type="number" min="1" max="1000" defaultValue={String(current?.priority ?? index + 1)} /><Input aria-label={`${target.upstream_id} weight`} name={`weight:${target.id}`} type="number" min="1" max="10000" defaultValue={String(current?.weight ?? 1)} /></div>;
              })}
            </div>
            <p className="help-text">{routingHelp} Free-only prices must have manager-recorded zero rates verified within 24 hours.</p>
            <Button disabled={dashboard.busy}>Save route</Button>
          </form>
        </details>
      )}
      {preview && <div className="route-preview" role="status"><strong>Selected: {preview.route.targets[0]?.upstream_id ?? "none"}</strong><small>{preview.route.selection_reason}</small><small>{preview.input.operation} · {preview.input.streaming ? "streaming" : "non-streaming"} · {preview.input.estimated_input_tokens} input / {preview.input.estimated_output_tokens} output tokens</small>{preview.route.rejected.map((entry) => <small key={`${entry.connection_id}:${entry.upstream_model_id}`}>{entry.upstream_model_id}: {entry.reason.replaceAll("_", " ")}</small>)}</div>}
    </Card>
  );
}
