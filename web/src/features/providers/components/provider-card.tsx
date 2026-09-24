import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/providers/components/provider-form";
import { ProviderAdapterScriptEditor } from "@/features/providers/components/provider-adapter-script";
import type { ProviderAdapterScript, ProviderConnection } from "@/features/providers/types/providers.types";

type Props = {
  item: ProviderConnection;
  busy: boolean;
  onToggle: (item: ProviderConnection) => void;
  onCredential: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onAddModel: (event: FormEvent<HTMLFormElement>, id: string) => void;
  script: ProviderAdapterScript | null | undefined;
  onLoadScript: (item: ProviderConnection) => void;
  onSaveScript: (event: FormEvent<HTMLFormElement>, item: ProviderConnection) => void;
  onRemoveScript: (item: ProviderConnection) => void;
};

export function ProviderCard({ item, busy, onToggle, onCredential, onAddModel, script, onLoadScript, onSaveScript, onRemoveScript }: Props) {
  return (
    <Card className="panel">
      <div className="resource-row-main">
        <div className="grid gap-1"><strong>{item.name}</strong><small>{item.preset} · {item.adapter_label} · {item.base_url} · credential {item.credential_state} · {item.enabled ? "enabled" : "disabled"}</small><code>{item.id}</code></div>
        <Button variant="outline" disabled={busy} onClick={() => onToggle(item)}>{item.enabled ? "Disable" : "Enable"}</Button>
      </div>
      <div className="provider-actions">
      {item.credential_required && (
        <details className="grant-editor">
          <summary>Replace credential</summary>
          <form onSubmit={(event) => onCredential(event, item.id)}>
            <div className="inline-fields">
              <div className="field"><Label htmlFor={`mode-${item.id}`}>Storage</Label><select id={`mode-${item.id}`} className="select" name="mode"><option value="stored">Encrypted local value</option><option value="external">External reference</option></select></div>
              <Field id={`credential-${item.id}`} name="value" label="Credential or external reference" type="password" placeholder="env:NAME or bearer-file:/run/secrets/token" required />
            </div>
            <Button disabled={busy}>Replace credential</Button>
          </form>
        </details>
      )}
      <ProviderAdapterScriptEditor item={item} script={script} busy={busy} onOpen={onLoadScript} onSave={onSaveScript} onRemove={onRemoveScript} />
      <details className="grant-editor">
        <summary>Add upstream model</summary>
        <form onSubmit={(event) => onAddModel(event, item.id)}>
          <Field id={`upstream-${item.id}`} name="upstream_id" label="Upstream model ID" required />
          <fieldset className="scope-grid"><legend>Capabilities</legend>{item.capability_details.map((capability) => <label key={capability.id}><input type="checkbox" name="capabilities" value={capability.id} /><span>{capability.label}</span></label>)}</fieldset>
          <Button disabled={busy}>Add upstream model</Button>
        </form>
      </details>
      </div>
    </Card>
  );
}
