import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { ModelField } from "@/features/models/components/model-fields";
import type { useModels } from "@/features/models/hooks/use-models";

type ModelsModel = ReturnType<typeof useModels>;

export function PublishModelPanel({ model }: { model: ModelsModel }) {
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    const published = await model.publishModel({
      id: String(form.get("id")),
      label: String(form.get("label")),
      description: String(form.get("description")),
      target_model_id: String(form.get("target_model_id")),
      capabilities: form.getAll("capabilities").map(String),
    });
    if (published) element.reset();
  }

  return (
    <Card className="panel">
      <h2>Publish model</h2>
      <form onSubmit={submit}>
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
          {model.publishCapabilities.map((capability) => <label key={capability.id}><input name="capabilities" type="checkbox" value={capability.id} /><span>{capability.label}</span></label>)}
        </fieldset>
        <Button disabled={model.busy}>Publish model</Button>
      </form>
    </Card>
  );
}
