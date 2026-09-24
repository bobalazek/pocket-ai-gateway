import type { MouseEvent } from "react";

import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { RequestAttemptDetails } from "@/features/requests/components/request-attempt";
import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { requestListFilters, requestSearch } from "@/features/requests/utils/requests.utils";

export function RequestList({ items, filters, next, onNavigate, onOpenDetail }: { items: GatewayRequest[]; filters: RequestFilters; next: string; onNavigate: (filters: RequestFilters) => void; onOpenDetail: (id: string) => void }) {
  return (
    <section className="section-block">
      <h2>{items.length} requests</h2>
      <div className="resource-list">
        {items.map((item) => <RequestCard key={item.id} item={item} filters={filters} onOpenDetail={onOpenDetail} />)}
      </div>
      <div className="row-actions section-block">
        {filters.cursor && <Button variant="outline" onClick={() => window.history.back()}>Previous page</Button>}
        {next && <Button variant="outline" onClick={() => onNavigate({ ...filters, cursor: next })}>Next page</Button>}
      </div>
    </section>
  );
}

function RequestCard({ item, filters, onOpenDetail }: { item: GatewayRequest; filters: RequestFilters; onOpenDetail: (id: string) => void }) {
  function openDetail(event: MouseEvent<HTMLAnchorElement>) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onOpenDetail(item.id);
  }

  const detailQuery = requestSearch({ ...requestListFilters(filters), request_id: item.id });
  return (
    <Card className="panel">
      <div className="resource-row-main">
        <div className="grid gap-1">
          <strong>{item.model_id}</strong>
          <small>{item.dialect} · {item.operation} · {item.state} · {new Date(item.started_at).toLocaleString()}</small>
          <code>{item.id}</code>
        </div>
        <div className="row-actions">
          <span className="status-badge">{item.attempts.length} attempt{item.attempts.length === 1 ? "" : "s"}</span>
          <a className={buttonVariants({ variant: "outline" })} href={`?${detailQuery}`} onClick={openDetail}>View details</a>
        </div>
      </div>
      {item.attempts.map((attempt) => <RequestAttemptDetails key={attempt.id} attempt={attempt} clientDialect={item.dialect} />)}
    </Card>
  );
}
