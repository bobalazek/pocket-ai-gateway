"use client";

import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { ProviderCard } from "@/features/providers/components/provider-card";
import { ProviderForm } from "@/features/providers/components/provider-form";
import { useProviders } from "@/features/providers/hooks/use-providers";

export default function ProvidersPage() {
  const model = useProviders();
  if (model.user?.role === "member") {
    return <AppShell active="Providers"><main id="main-content" className="content"><h1>Administrator access required.</h1></main></AppShell>;
  }

  return (
    <AppShell active="Providers">
      <main id="main-content" className="content management-page">
        <header className="page-header">
          <div><p className="context">Provider boundary</p><h1>Providers</h1><p className="lede">Connect native APIs. Stored credentials are encrypted and never shown again.</p></div>
        </header>
        {model.error && <p className="form-error" role="alert">{model.error}</p>}
        <ProviderForm presets={model.presets} adapters={model.adapters} selected={model.selected} selectedPreset={model.selectedPreset} adapter={model.adapter} busy={model.busy} onAdapter={model.setAdapter} onPreset={model.choosePreset} onSubmit={model.create} />
        <section className="section-block">
          <h2>{model.items.length} connections</h2>
          <div className="resource-list">
            {model.items.map((item) => <ProviderCard key={item.id} item={item} busy={model.busy} onToggle={model.toggle} onCredential={model.credential} onAddModel={model.addModel} />)}
          </div>
        </section>
        {model.items.length > 0 && <div className="next-step"><div><strong>Next: publish a model</strong><small>Map a stable public model name to one of these upstream models.</small></div><Link className={buttonVariants()} href="/models/">Continue to models</Link></div>}
      </main>
    </AppShell>
  );
}
