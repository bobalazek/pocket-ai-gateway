import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { ProviderAdapter, ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";

type Props = {
  presets: ProviderPreset[];
  adapters: ProviderAdapter[];
  selected?: ProviderPreset;
  selectedPreset: string;
  adapter: ProviderConnection["adapter"];
  busy: boolean;
  onAdapter: (value: ProviderConnection["adapter"]) => void;
  onPreset: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
};

export function ProviderForm({ presets, adapters, selected, selectedPreset, adapter, busy, onAdapter, onPreset, onSubmit }: Props) {
  return (
    <Card className="panel">
      <h2>Add connection</h2>
      <form onSubmit={onSubmit}>
        <div className="inline-fields">
          <Field id="name" label="Name" required />
          <div className="field">
            <Label htmlFor="preset">Provider preset</Label>
            <select id="preset" name="preset" className="select" value={selectedPreset || "custom"} onChange={(event) => onPreset(event.target.value)}>
              <option value="custom">Custom</option>
              {presets.map((preset) => <option key={preset.id} value={preset.id}>{preset.label}</option>)}
            </select>
          </div>
        </div>
        {selected && <p className="help-text">Supports {selected.capability_details.map((capability) => capability.label).join(", ")}{selected.authentication ? `. ${selected.authentication}` : ""}. <a href={selected.documentation_url} target="_blank" rel="noreferrer">Provider documentation</a> · reviewed {selected.reviewed_at}</p>}
        <div className="inline-fields">
          <div className="field">
            <Label htmlFor="adapter">Adapter</Label>
            {selected ? <><input type="hidden" name="adapter" value={adapter} /><Input id="adapter" value={selected.adapter_label} readOnly /></> : <select id="adapter" name="adapter" className="select" value={adapter} onChange={(event) => onAdapter(event.target.value as ProviderConnection["adapter"])}>
              {adapters.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}
            </select>}
          </div>
          {selected?.base_url ? <div className="field"><Label htmlFor="base_url">Base URL</Label><Input id="base_url" name="base_url" type="url" value={selected.base_url} readOnly /></div> : <Field id="base_url" label={selected?.base_url_required ? "Resource base URL" : "Versioned base URL"} type="url" placeholder={selected?.base_url_example} required />}
        </div>
        <div className="inline-fields">
          <Field id="timeout_ms" label="Timeout (ms)" type="number" defaultValue="60000" required />
          {selected
            ? <Switch checked={selected.private_network} disabled readOnly label="Allow local or private network" description={`Set by the ${selected.label} preset`} />
            : <Switch name="allow_private_network" label="Allow local or private network" description="Needed for servers on this machine or your LAN, such as Ollama" />}
        </div>
        <Button disabled={busy || !adapter}>Add connection</Button>
      </form>
    </Card>
  );
}

export function Field({ id, name, label, type = "text", placeholder, defaultValue, required = false }: { id: string; name?: string; label: string; type?: string; placeholder?: string; defaultValue?: string; required?: boolean }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={name ?? id} type={type} placeholder={placeholder} defaultValue={defaultValue} required={required} /></div>;
}
