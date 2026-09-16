import Link from "next/link";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ModelField } from "@/features/models/components/model-fields";
import type { useModels } from "@/features/models/hooks/use-models";
import type { CatalogCandidate } from "@/features/models/types/models.types";

type ModelsModel = ReturnType<typeof useModels>;

export function CatalogPanel({ model }: { model: ModelsModel }) {
  if (!model.catalogState) return null;
  const state = model.catalogState;
  return (
    <Card className="panel">
      <h2>Provider catalog</h2>
      <p className="help-text">Refresh imports bounded metadata candidates only. Review a candidate below, then <Link href="/providers/">add its model to the matching provider</Link> and publish the stable public ID above. Local model and price entries remain authoritative.</p>
      <form onSubmit={model.updateCatalog}>
        <ModelField id="source_url" label="GitHub catalog JSON URL" type="url" defaultValue={state.source_url} />
        <div className="inline-fields"><ModelField id="refresh_interval_hours" label="Refresh interval (hours)" type="number" defaultValue={String(state.refresh_interval_hours)} required /><label className="checkbox-row"><input name="refresh_enabled" type="checkbox" defaultChecked={state.refresh_enabled} /> Enable scheduled refresh</label></div>
        <div className="button-row"><Button disabled={model.busy}>Save catalog settings</Button><Button type="button" variant="outline" disabled={model.busy || !state.source_url} onClick={model.refreshCatalog}>Refresh now</Button></div>
      </form>
      <small>{model.catalog.length} loaded candidates · version {state.source_version || "not loaded"}{state.last_error ? ` · ${state.last_error}` : ""}</small>
      {model.catalog.length > 0 && <div className="catalog-candidates" aria-label="Catalog candidates">{model.catalog.map((candidate) => <div className="catalog-candidate" key={`${candidate.provider}:${candidate.model_id}`}><div><strong>{candidate.label}</strong><code>{candidate.model_id}</code><small>{candidate.provider} · {candidate.capability_details.map((capability) => capability.label).join(", ")}</small></div><div><strong>{candidate.free ? "Verified free" : formatCatalogPrice(candidate)}</strong><small>Source {candidate.source_version} · {new Date(candidate.discovered_at).toLocaleString()}</small></div></div>)}{model.catalogCursor && <Button type="button" variant="outline" disabled={model.busy} onClick={model.loadMoreCatalog}>Load more candidates</Button>}</div>}
    </Card>
  );
}

function formatCatalogPrice(candidate: CatalogCandidate) {
  if (candidate.input_nanos_per_million === undefined || candidate.output_nanos_per_million === undefined) return "Price unknown";
  return `$${candidate.input_nanos_per_million / 1_000_000_000} in · $${candidate.output_nanos_per_million / 1_000_000_000} out / 1M`;
}
