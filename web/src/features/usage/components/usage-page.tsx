"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { AdjustmentForm, RepriceForm, UnknownRow } from "@/features/usage/components/usage-adjustments";
import { UsageLimits } from "@/features/usage/components/usage-limits";
import { UsageOverview } from "@/features/usage/components/usage-overview";
import { PolicyForm, PriceSection } from "@/features/usage/components/usage-policy-pricing";
import { useUsage } from "@/features/usage/hooks/use-usage";

export default function UsagePage() {
  const model = useUsage();
  return (
    <AppShell active="Usage">
      <main id="main-content" className="content management-page">
        <header className="page-header"><div><p className="context">Accounting</p><h1>Usage and limits</h1><p className="lede">Local usage, enforceable limits, and effective-dated prices.</p></div></header>
        {model.error && <p className="form-error" role="alert">{model.error}</p>}
        {model.notice && <p className="form-success" role="status">{model.notice}</p>}
        <UsageOverview model={model} />
        <UsageLimits model={model} />
        {model.canManage && <PolicyForm isOwner={model.isOwner} busy={model.busy} onSubmit={model.createPolicy} />}
        {model.canManage && <PriceSection prices={model.prices} outbox={model.outbox} cursor={model.priceCursor} preview={model.pricePreview} busy={model.busy} onLoadMore={() => model.loadMore("prices")} onSubmit={model.createPrice} onCancel={() => model.setPricePreview(null)} />}
        {model.isOwner && <RepriceForm preview={model.reprice} busy={model.busy} onPreview={model.previewReprice} onApply={model.applyReprice} onCancel={() => model.setReprice(null)} />}
        {model.canManage && <AdjustmentForm preview={model.adjustment} busy={model.busy} onSubmit={model.adjust} onCancel={() => model.setAdjustment(null)} />}
        <section className="section-block">
          <h2>Unknown usage</h2>
          {model.unresolved.length ? <div className="resource-list">{model.unresolved.map((attempt) => <UnknownRow key={attempt.id} attempt={attempt} preview={model.reconciliation?.attempt_id === attempt.id ? model.reconciliation : null} canManage={model.canManage} busy={model.busy} onSubmit={model.reconcile} onCancel={() => model.setReconciliation(null)} />)}</div> : <p className="empty-copy">No unresolved attempts.</p>}
          {model.unresolvedCursor && <Button className="section-block" variant="outline" disabled={model.busy} onClick={() => model.loadMore("unresolved")}>Load more unresolved usage</Button>}
        </section>
      </main>
    </AppShell>
  );
}
