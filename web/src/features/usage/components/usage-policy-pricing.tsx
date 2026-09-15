import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/usage/components/usage-fields";
import type { OutboxStatus, PricePreview, PriceVersion } from "@/features/usage/types/usage.types";
import { policyKinds } from "@/features/usage/utils/usage.utils";

export function PolicyForm({ isOwner, busy, onSubmit }: { isOwner: boolean; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return (
    <Card className="panel">
      <h2>Create policy</h2>
      <form onSubmit={onSubmit}>
        <div className="inline-fields">
          <div className="field">
            <Label htmlFor="scope_kind">Scope</Label>
            <select className="select" id="scope_kind" name="scope_kind">
              {isOwner && <option value="instance">Instance</option>}
              <option value="user">User</option><option value="key">API key</option><option value="connection">Connection</option>
            </select>
          </div>
          <Field id="scope_id" label="Scope ID" />
        </div>
        <div className="inline-fields">
          <div className="field"><Label htmlFor="kind">Policy type</Label><select className="select" id="kind" name="kind">{Object.keys(policyKinds).map((kind) => <option key={kind} value={kind}>{kind.replaceAll("_", " ")}</option>)}</select></div>
          <div className="field"><Label htmlFor="limit">Limit</Label><Input id="limit" name="limit" type="number" step="any" min="0.000000001" defaultValue="100" required /></div>
        </div>
        <div className="inline-fields">
          <div className="field"><Label htmlFor="period">Quota period</Label><select className="select" id="period" name="period" defaultValue="day"><option>hour</option><option>day</option><option>week</option><option>month</option><option>lifetime</option></select></div>
          <Field id="window_seconds" label="Window seconds" type="number" />
        </div>
        <div className="inline-fields"><Field id="refill_units" label="Refill units" type="number" /><Field id="refill_interval_ms" label="Refill interval (ms)" type="number" /></div>
        <Button disabled={busy}>Create policy</Button>
      </form>
    </Card>
  );
}

type PriceProps = {
  prices: PriceVersion[];
  outbox: OutboxStatus | null;
  cursor: string;
  preview: PricePreview | null;
  busy: boolean;
  onLoadMore: () => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  onCancel: () => void;
};

export function PriceSection({ prices, outbox, cursor, preview, busy, onLoadMore, onSubmit, onCancel }: PriceProps) {
  return (
    <section className="section-block">
      <div className="section-heading">
        <div><p className="context">Price provenance</p><h2>Effective prices</h2></div>
        <small>{(outbox?.pending_events ?? 0) + (outbox?.reserved_events ?? 0)} events pending or reserved</small>
      </div>
      <div className="resource-list">
        {prices.map((price) => <Card className="resource-row" key={price.id}><div><strong>{price.model_id}</strong><small>{price.connection_id} · ${price.input_usd_per_million} / ${price.output_usd_per_million} per million · {price.source}</small><small>{new Date(price.effective_from).toLocaleString()} → {price.effective_to ? new Date(price.effective_to).toLocaleString() : "current"}</small></div></Card>)}
      </div>
      {cursor && <Button className="section-block" variant="outline" disabled={busy} onClick={onLoadMore}>Load more prices</Button>}
      <Card className="panel section-block">
        <h2>Add price version</h2>
        <form onSubmit={onSubmit}>
          <div className="inline-fields"><Field id="price_connection" name="connection_id" label="Connection ID" required disabled={!!preview} /><Field id="price_model" name="model_id" label="Model ID" required disabled={!!preview} /></div>
          <div className="inline-fields"><Field id="input_price" label="Input USD / million" required disabled={!!preview} /><Field id="output_price" label="Output USD / million" required disabled={!!preview} /></div>
          <Field id="source" label="Source" required disabled={!!preview} />
          <div className="inline-fields"><Field id="effective_from" label="Effective from" type="datetime-local" required disabled={!!preview} /><Field id="effective_to" label="Effective to" type="datetime-local" disabled={!!preview} /></div>
          {preview && <p className="form-success" role="status">Confirm {preview.connection_id} / {preview.model_id}: ${preview.input_usd_per_million} input, ${preview.output_usd_per_million} output · {preview.source} · from {new Date(preview.effective_from).toLocaleString()} to {preview.effective_to ? new Date(preview.effective_to).toLocaleString() : "current"}?</p>}
          <div className="row-actions"><Button disabled={busy}>{preview ? "Confirm price" : "Preview price"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div>
        </form>
      </Card>
    </section>
  );
}
