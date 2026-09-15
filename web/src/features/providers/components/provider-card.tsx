import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/providers/components/provider-form";
import type { ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";
import { availableCapabilities } from "@/features/providers/utils/provider-capabilities.utils";

type Props = {
  item: ProviderConnection;
  presets: ProviderPreset[];
  busy: boolean;
  onToggle: (item: ProviderConnection) => void;
  onCredential: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onAddModel: (event: FormEvent<HTMLFormElement>, id: string) => void;
};

export function ProviderCard({ item, presets, busy, onToggle, onCredential, onAddModel }: Props) {
  return (
    <Card className="panel">
      <div className="resource-row-main">
        <div><strong>{item.name}</strong><small>{item.preset} · {item.adapter.replaceAll("_", " ")} · {item.base_url} · credential {item.credential_state} · {item.enabled ? "enabled" : "disabled"}</small><code>{item.id}</code></div>
        <Button variant="outline" disabled={busy} onClick={() => onToggle(item)}>{item.enabled ? "Disable" : "Enable"}</Button>
      </div>
      {item.preset !== "ollama" && (
        <details className="grant-editor">
          <summary>Replace credential</summary>
          <form onSubmit={(event) => onCredential(event, item.id)}>
            <div className="inline-fields">
              <div className="field"><Label htmlFor={`mode-${item.id}`}>Storage</Label><select id={`mode-${item.id}`} className="select" name="mode"><option value="stored">Encrypted local value</option><option value="external">Environment reference</option></select></div>
              <Field id={`credential-${item.id}`} name="value" label="Credential or env:NAME" type="password" required />
            </div>
            <Button disabled={busy}>Replace credential</Button>
          </form>
        </details>
      )}
      <details className="grant-editor">
        <summary>Add upstream model</summary>
        <form onSubmit={(event) => onAddModel(event, item.id)}>
          <Field id={`upstream-${item.id}`} name="upstream_id" label="Upstream model ID" required />
          {item.preset === "openai" && <p className="help-text">Image edits support GPT Image models; variations require <code>dall-e-2</code>; audio translation requires <code>whisper-1</code>.</p>}
          <fieldset className="scope-grid"><legend>Capabilities</legend>{availableCapabilities(item, presets).map((value) => <label key={value}><input type="checkbox" name="capabilities" value={value} /><span>{value.replaceAll("_", " ")}</span></label>)}</fieldset>
          {(item.preset === "openai" || item.preset === "anthropic") && <p className="help-text">Hosted web search requires both <code>chat</code> and <code>web_search</code>. Anthropic supports direct JSON or SSE <code>web_search_20250305</code> requests with an explicit 1–4 use limit and no prompt-cache controls.</p>}
          <Button disabled={busy}>Add upstream model</Button>
        </form>
      </details>
    </Card>
  );
}
