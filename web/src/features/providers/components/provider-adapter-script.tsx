import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import type { ProviderAdapterScript, ProviderConnection } from "@/features/providers/types/providers.types";

const requestExample = `(input) => ({
  method: input.method,
  path: input.path,
  headers: input.headers,
  body: input.body,
})`;

const responseExample = `(input) => ({
  status: input.status,
  headers: input.headers,
  body: input.body,
})`;

type Props = {
  item: ProviderConnection;
  script: ProviderAdapterScript | null | undefined;
  busy: boolean;
  onOpen: (item: ProviderConnection) => void;
  onSave: (event: FormEvent<HTMLFormElement>, item: ProviderConnection) => void;
  onRemove: (item: ProviderConnection) => void;
};

export function ProviderAdapterScriptEditor({ item, script, busy, onOpen, onSave, onRemove }: Props) {
  if (item.preset !== "custom" || item.adapter !== "openai_compatible") return null;
  return (
    <details className="grant-editor" onToggle={(event) => { if (event.currentTarget.open) void onOpen(item); }}>
      <summary>JavaScript adapter</summary>
      <p className="help-text">Trusted owner/admin code only. Scripts cannot access files, processes, modules, timers, credentials, or the network, but the embedded VM shares the server memory heap.</p>
      {script === undefined ? <p className="fine-print">Loading adapter…</p> : (
        <form onSubmit={(event) => onSave(event, item)}>
          <div className="field">
            <Label htmlFor={`request-script-${item.id}`}>Request transform</Label>
            <textarea className="select code-input" id={`request-script-${item.id}`} name="request_script" rows={10} defaultValue={script?.request_script ?? requestExample} spellCheck={false} required />
          </div>
          <div className="field">
            <Label htmlFor={`response-script-${item.id}`}>Response transform</Label>
            <textarea className="select code-input" id={`response-script-${item.id}`} name="response_script" rows={10} defaultValue={script?.response_script ?? responseExample} spellCheck={false} required />
          </div>
          <div className="button-row">
            <Button disabled={busy}>Validate and save</Button>
            {script && <Button type="button" variant="outline" disabled={busy} onClick={() => onRemove(item)}>Remove script</Button>}
          </div>
        </form>
      )}
    </details>
  );
}
