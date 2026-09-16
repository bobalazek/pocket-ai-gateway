"use client";

import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { CatalogPanel } from "@/features/models/components/catalog-panel";
import { ModelCard } from "@/features/models/components/model-card";
import { ModelsSearch } from "@/features/models/components/models-search";
import { PublishModelPanel } from "@/features/models/components/publish-model-panel";
import { useModels } from "@/features/models/hooks/use-models";

export default function ModelsPage() {
  const model = useModels();
  return (
    <AppShell active="Models">
      <main id="main-content" className="content management-page">
        <header className="page-header">
          <div><p className="context">Stable catalog</p><h1>Models</h1><p className="lede">{model.manager ? "Publish stable names, choose eligible targets, and preview every routing decision." : "Models available within your account grants."}</p></div>
        </header>
        {model.error && <p className="form-error" role="alert">{model.error}</p>}
        {model.manager && <PublishModelPanel model={model} />}
        <ModelsSearch search={model.search} busy={model.busy} onSearch={model.searchModels} />
        <section className="section-block">
          <h2>{model.models.length} available models</h2>
          {model.models.length ? <div className="resource-list">{model.models.map((item) => <ModelCard key={item.id} item={item} dashboard={model} />)}</div> : !model.busy && <p className="empty-copy">No models match this search.</p>}
        </section>
        {model.manager && <CatalogPanel model={model} />}
        {model.manager && model.models.length > 0 && <div className="next-step"><div><strong>Next: create a scoped key</strong><small>Grant every connection that a model route may select.</small></div><Link className={buttonVariants()} href="/keys/">Continue to API keys</Link></div>}
      </main>
    </AppShell>
  );
}
