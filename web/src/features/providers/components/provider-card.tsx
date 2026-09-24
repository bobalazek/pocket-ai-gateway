import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/providers/components/provider-form";
import { ProviderAdapterScriptEditor } from "@/features/providers/components/provider-adapter-script";
import type { ProviderAdapterScript, ProviderConnection, UpstreamModel } from "@/features/providers/types/providers.types";

type Props = {
  item: ProviderConnection;
  models: UpstreamModel[] | null | undefined;
  busy: boolean;
  onToggle: (item: ProviderConnection) => void;
  onCredential: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onAddModel: (event: FormEvent<HTMLFormElement>, id: string) => void;
  script: ProviderAdapterScript | null | undefined;
  onLoadScript: (item: ProviderConnection) => void;
  onSaveScript: (event: FormEvent<HTMLFormElement>, item: ProviderConnection) => void;
  onRemoveScript: (item: ProviderConnection) => void;
};

export function ProviderCard({ item, models, busy, onToggle, onCredential, onAddModel, script, onLoadScript, onSaveScript, onRemoveScript }: Props) {
  return (
    <Card className="panel">
      <div className="resource-row-main">
        <div className="grid min-w-0 gap-1"><strong>{item.name}</strong><small>{item.preset} · {item.adapter_label} · credential {item.credential_state} · {item.enabled ? "enabled" : "disabled"}</small><span className="text-xs text-[var(--muted)]">Base URL</span><code>{item.base_url}</code></div>
        <Button variant="outline" disabled={busy} onClick={() => onToggle(item)}>{item.enabled ? "Disable" : "Enable"}</Button>
      </div>
      <div className="mt-5 border-t border-[var(--border)] pt-4">
        <h3 className="font-semibold">Upstream models{models ? ` · ${models.length}` : ""}</h3>
        {models === null ? <p className="help-text">Model list unavailable. Refresh the page to retry.</p> : models === undefined ? <p className="help-text">Loading models…</p> : models.length === 0 ? <p className="help-text">No upstream models yet. Add one below, then publish it on Models.</p> : (
          <ul className="mt-3 grid gap-2">
            {models.map((model) => <li key={model.id} className="rounded-lg border border-[var(--border)] px-3 py-2">
              <code className="break-all text-sm">{model.upstream_id}</code>
              <div className="mt-1 flex flex-wrap gap-x-2 gap-y-1 text-xs text-[var(--muted)]">{model.capability_details.map((capability) => <span key={capability.id}>{capability.label}</span>)}</div>
            </li>)}
          </ul>
        )}
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
          <p className="help-text">Models on this connection may require different provider-defined inputs. Check the model’s documentation before sending requests.</p>
          <fieldset className="scope-grid"><legend>Capabilities</legend>{item.capability_details.map((capability) => <label key={capability.id}><input type="checkbox" name="capabilities" value={capability.id} /><span>{capability.label}</span></label>)}</fieldset>
          <Button disabled={busy}>Add upstream model</Button>
        </form>
      </details>
      </div>
    </Card>
  );
}
