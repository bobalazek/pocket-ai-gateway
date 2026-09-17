import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { useSettings } from "@/features/settings/hooks/use-settings";

type SettingsModel = ReturnType<typeof useSettings>;

export function ConfigTransfer({ model }: { model: SettingsModel }) {
  const { pendingFile, preview, busy, previewGeneration, downloadConfig, chooseConfig, applyConfig, setPendingConfig, setPendingFile, setPreview } = model;
  const invalidatePreview = () => {
    previewGeneration.current++;
    setPendingConfig(null);
    setPendingFile("");
    setPreview(null);
  };

  return (
    <section className="settings-grid section-block">
      <Card className="panel">
        <h2>Export configuration</h2>
        <p>Download providers, adapter scripts, models, routes, policies, prices, catalog, and operations settings. Secrets and identities are excluded.</p>
        <Button type="button" variant="outline" disabled={busy} onClick={downloadConfig}>Export JSON</Button>
      </Card>
      <Card className="panel">
        <h2>Import configuration</h2>
        <form onSubmit={chooseConfig}>
          <div className="field"><Label htmlFor="config">Configuration JSON</Label><Input id="config" name="config" type="file" accept="application/json,.json" required disabled={busy} onChange={invalidatePreview} /></div>
          {preview && <p className="form-success" role="status">Ready from {pendingFile}: {preview.connections} providers, {preview.adapter_scripts} adapter scripts, {preview.public_models} models, {preview.policies} policies, and {preview.prices} prices. Credentials stay local; changed endpoints require credentials again.</p>}
          <div className="row-actions"><Button disabled={busy}>{preview ? "Preview another" : "Preview import"}</Button>{preview && <Button type="button" disabled={busy} onClick={applyConfig}>Apply {pendingFile}</Button>}</div>
        </form>
      </Card>
    </section>
  );
}
