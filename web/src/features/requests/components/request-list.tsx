import type { MouseEvent } from "react";

import { Button, buttonVariants } from "@/components/ui/button";
import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { requestListFilters, requestSearch } from "@/features/requests/utils/requests.utils";

export function RequestList({ items, filters, next, onNavigate, onOpenDetail }: { items: GatewayRequest[]; filters: RequestFilters; next: string; onNavigate: (filters: RequestFilters) => void; onOpenDetail: (id: string) => void }) {
  return (
    <section className="section-block request-list" aria-labelledby="request-list-title">
      <h2 id="request-list-title">{items.length} {items.length === 1 ? "request" : "requests"} on this page</h2>
      {items.length ? (
        <>
          <p className="help-text request-list-note">Tokens, cost, and provider show the latest attempt. Open details for full accounting.</p>
          <div className="table-wrap request-table-wrap" role="region" aria-label="Scrollable request history" tabIndex={0}>
            <table className="request-table">
              <caption className="sr-only">Request history</caption>
              <thead><tr><th scope="col">Model</th><th scope="col">State</th><th scope="col">Started</th><th scope="col">Tokens</th><th scope="col">Cost</th><th scope="col">Provider</th><th scope="col"><span className="sr-only">Details</span></th></tr></thead>
              <tbody>{items.map((item) => <RequestRow key={item.id} item={item} filters={filters} onOpenDetail={onOpenDetail} />)}</tbody>
            </table>
          </div>
        </>
      ) : <p className="empty-copy">No requests match these filters.</p>}
      <div className="row-actions section-block">
        {filters.cursor && <Button variant="outline" onClick={() => window.history.back()}>Previous page</Button>}
        {next && <Button variant="outline" onClick={() => onNavigate({ ...filters, cursor: next })}>Next page</Button>}
      </div>
    </section>
  );
}

function RequestRow({ item, filters, onOpenDetail }: { item: GatewayRequest; filters: RequestFilters; onOpenDetail: (id: string) => void }) {
  function openDetail(event: MouseEvent<HTMLAnchorElement>) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onOpenDetail(item.id);
  }

  const detailQuery = requestSearch({ ...requestListFilters(filters), request_id: item.id });
  const latest = item.attempts[item.attempts.length - 1];
  const tokens = latest?.input_tokens != null && latest.output_tokens != null
    ? (latest.input_tokens + latest.output_tokens).toLocaleString()
    : "Unknown";
  return (
    <tr>
      <td className="request-model-cell" data-label="Model"><strong>{item.model_id}</strong><small>{item.dialect} · {item.operation}</small><code>{item.id}</code>{item.attempts.length > 1 && <small>{item.attempts.length} attempts</small>}</td>
      <td data-label="State"><span className="request-state" data-state={item.state}>{item.state}</span></td>
      <td data-label="Started"><time dateTime={item.started_at}>{new Date(item.started_at).toLocaleString()}</time></td>
      <td data-label="Tokens">{tokens}</td>
      <td data-label="Cost">{latest?.cost_usd == null ? "Unknown" : `$${latest.cost_usd}`}</td>
      <td className="request-provider-cell" data-label="Provider">{latest?.connection_id ? <code>{latest.connection_id}</code> : "Unknown"}</td>
      <td className="request-action-cell"><a className={buttonVariants({ variant: "outline" })} href={`?${detailQuery}`} onClick={openDetail} aria-label={`View details for request ${item.id}`}>View details</a></td>
    </tr>
  );
}
