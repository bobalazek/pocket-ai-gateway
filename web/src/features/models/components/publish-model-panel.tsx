import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { ModelField, modelCapabilities } from "@/features/models/components/model-fields";
import type { useModels } from "@/features/models/hooks/use-models";

type ModelsModel = ReturnType<typeof useModels>;

export function PublishModelPanel({ model }: { model: ModelsModel }) {
  return (
    <Card className="panel">
      <h2>Publish model</h2>
      <form onSubmit={model.create}>
        <div className="inline-fields"><ModelField id="id" label="Public model ID" required /><ModelField id="label" label="Display name" required /></div>
        <ModelField id="description" label="Description" />
        <div className="field">
          <Label htmlFor="target_model_id">Initial upstream target</Label>
          <select id="target_model_id" name="target_model_id" className="select" value={model.selectedTarget} onChange={(event) => model.setSelectedTarget(event.target.value)} required>
            <option value="">Select a target</option>
            {model.targets.map((item) => <option key={item.id} value={item.id}>{item.upstream_id} · {item.connection_id}</option>)}
          </select>
        </div>
        <fieldset className="scope-grid">
          <legend>Published capabilities</legend>
          {modelCapabilities.filter((value) => model.publishCapabilities.includes(value)).map((value) => <label key={value}><input name="capabilities" type="checkbox" value={value} /><span>{value.replaceAll("_", " ")}</span></label>)}
        </fieldset>
        {model.publishCapabilities.includes("web_search") && <p className="help-text">Web search requires both <code>chat</code> and <code>web_search</code>.</p>}
        <Button disabled={model.busy}>Publish model</Button>
      </form>
    </Card>
  );
}
