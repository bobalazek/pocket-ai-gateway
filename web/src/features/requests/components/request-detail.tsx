import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { RequestAttemptDetails } from "@/features/requests/components/request-attempt";
import type { GatewayRequest } from "@/features/requests/types/requests.types";

export function RequestDetail({ item, loading, error, onClose }: { item?: GatewayRequest; loading: boolean; error?: string; onClose: () => void }) {
  return (
    <section className="section-block" aria-label="Request detail">
      <Button type="button" variant="outline" onClick={onClose}>Back to requests</Button>
      {error ? (
        <p className="form-error section-block" role="alert">{error}</p>
      ) : loading ? (
        <p className="empty-copy section-block" role="status">Loading request…</p>
      ) : item ? (
        <Card className="panel section-block">
          <div className="resource-row-main">
            <div className="grid gap-1">
              <p className="context">Request outcome</p>
              <h2 id="request-detail-heading">{item.model_id}</h2>
              <small>{item.dialect} · {item.operation} · {new Date(item.started_at).toLocaleString()}</small>
              <code>{item.id}</code>
            </div>
            <span className="status-badge">{item.state}</span>
          </div>
          <dl className="facts section-block">
            <div><dt>User</dt><dd>{item.owner_user_id}</dd></div>
            <div><dt>API key</dt><dd>{item.key_id}</dd></div>
            <div><dt>Attempts</dt><dd>{item.attempts.length}</dd></div>
          </dl>
          {item.attempts.map((attempt) => <RequestAttemptDetails key={attempt.id} attempt={attempt} clientDialect={item.dialect} />)}
          <p className="help-text">Request content was not captured.</p>
        </Card>
      ) : (
        <p className="empty-copy section-block">This request was not found or is no longer available.</p>
      )}
    </section>
  );
}
