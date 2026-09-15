import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";

type Props = {
  presets: ProviderPreset[];
  selected?: ProviderPreset;
  selectedPreset: string;
  adapter: ProviderConnection["adapter"];
  busy: boolean;
  onAdapter: (value: ProviderConnection["adapter"]) => void;
  onPreset: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
};

export function ProviderForm({ presets, selected, selectedPreset, adapter, busy, onAdapter, onPreset, onSubmit }: Props) {
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
        {selected && <p className="help-text">Uses {selected.adapter.replaceAll("_", " ")}{selected.base_url ? ` at ${selected.base_url}` : " with your resource URL"}. Supported gateway operations: {selected.operations.join(", ")}. <a href={selected.documentation_url} target="_blank" rel="noreferrer">Provider documentation</a> reviewed {selected.reviewed_at}.</p>}
        <div className="inline-fields">
          <div className="field">
            <Label htmlFor="adapter">Adapter</Label>
            {selected && <input type="hidden" name="adapter" value={adapter} />}
            <select id="adapter" name={selected ? undefined : "adapter"} className="select" value={adapter} disabled={Boolean(selected)} onChange={(event) => onAdapter(event.target.value as ProviderConnection["adapter"])}>
              <option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="gemini">Gemini</option><option value="openai_compatible">OpenAI compatible</option>
            </select>
          </div>
          {selected?.base_url ? <div className="field"><Label htmlFor="base_url">Base URL</Label><Input id="base_url" name="base_url" type="url" value={selected.base_url} readOnly /></div> : <Field id="base_url" label={selected?.base_url_required ? "Resource base URL" : "Versioned base URL"} type="url" placeholder={selected?.base_url_required ? "https://your-resource.example/openai/v1" : "https://api.example.com/v1"} required />}
        </div>
        <div className="inline-fields">
          <Field id="timeout_ms" label="Timeout (ms)" type="number" defaultValue="60000" required />
          {selected ? <label className="checkbox-row"><input type="checkbox" checked={selected.private_network} disabled readOnly /> {selected.private_network ? "Local/private network enabled by preset" : "Public HTTPS endpoint"}</label> : <label className="checkbox-row"><input type="checkbox" name="allow_private_network" /> Allow local/private HTTP for this connection</label>}
        </div>
        <Button disabled={busy}>Add connection</Button>
      </form>
      <p className="help-text">Presets pin the reviewed adapter and endpoint rules. Ollama uses a local endpoint without credentials. Cloud presets require the resource URL shown by the provider.</p>
    </Card>
  );
}

export function Field({ id, name, label, type = "text", placeholder, defaultValue, required = false }: { id: string; name?: string; label: string; type?: string; placeholder?: string; defaultValue?: string; required?: boolean }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={name ?? id} type={type} placeholder={placeholder} defaultValue={defaultValue} required={required} /></div>;
}
