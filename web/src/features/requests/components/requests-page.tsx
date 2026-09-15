"use client";

import { AppShell } from "@/components/app-shell";
import { RequestDetail } from "@/features/requests/components/request-detail";
import { RequestFilterForm } from "@/features/requests/components/request-filters";
import { RequestList } from "@/features/requests/components/request-list";
import { useRequests } from "@/features/requests/hooks/use-requests";

export default function RequestsPage() {
  const { items, filters, next, error, loading, apply, navigate, openDetail, closeDetail } = useRequests();
  const selected = filters.request_id ? items.find((item) => item.id === filters.request_id) : undefined;
  return (
    <AppShell active="Requests">
      <main id="main-content" className="content management-page">
        <header className="page-header">
          <div><p className="context">Inference history</p><h1>Requests</h1><p className="lede">Metadata and accounting state. Request content is not captured.</p></div>
        </header>
        {error && !filters.request_id && <p className="form-error" role="alert">{error}</p>}
        {filters.request_id ? (
          <RequestDetail item={selected} loading={loading} error={error} onClose={closeDetail} />
        ) : (
          <>
            <RequestFilterForm filters={filters} onSubmit={apply} />
            <RequestList items={items} filters={filters} next={next} onNavigate={navigate} onOpenDetail={openDetail} />
          </>
        )}
      </main>
    </AppShell>
  );
}
