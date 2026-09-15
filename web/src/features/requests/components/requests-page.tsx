"use client";

import { AppShell } from "@/components/app-shell";
import { RequestFilterForm } from "@/features/requests/components/request-filters";
import { RequestList } from "@/features/requests/components/request-list";
import { useRequests } from "@/features/requests/hooks/use-requests";

export default function RequestsPage() {
  const { items, filters, next, error, apply, navigate } = useRequests();
  return (
    <AppShell active="Requests">
      <main id="main-content" className="content management-page">
        <header className="page-header">
          <div><p className="context">Inference history</p><h1>Requests</h1><p className="lede">Metadata and accounting state. Request content is not captured.</p></div>
        </header>
        {error && <p className="form-error" role="alert">{error}</p>}
        <RequestFilterForm filters={filters} onSubmit={apply} />
        <RequestList items={items} filters={filters} next={next} onNavigate={navigate} />
      </main>
    </AppShell>
  );
}
