import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/usage/components/usage-fields";
import type { OutboxStatus, PricePreview, PriceVersion } from "@/features/usage/types/usage.types";
import { formatWeeklyPriceWindow, weekdayOptions } from "@/features/usage/utils/pricing.utils";
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

function PriceCard({ price }: { price: PriceVersion }) {
  const cacheReadPrice = price.cache_read_usd_per_million == null ? "cache reads unpriced" : `$${price.cache_read_usd_per_million} cache read`;
  return (
    <Card className="resource-row">
      <div>
        <strong>{price.model_id}</strong>
        <small>{price.connection_id} · ${price.input_usd_per_million} input / {cacheReadPrice} / ${price.output_usd_per_million} output per million · {price.source}</small>
        <small>{formatWeeklyPriceWindow(price.weekly_start_minute_utc ?? null, price.weekly_end_minute_utc ?? null)}</small>
        <small>{new Date(price.effective_from).toLocaleString()} → {price.effective_to ? new Date(price.effective_to).toLocaleString() : "current"}</small>
      </div>
    </Card>
  );
}

function WeekdaySelect({ id, label, end = false, disabled }: { id: string; label: string; end?: boolean; disabled: boolean }) {
  return (
    <div className="field">
      <Label htmlFor={id}>{label}</Label>
      <select className="select" id={id} name={id} defaultValue="" disabled={disabled} aria-describedby="weekly-window-help">
        <option value="">Not set</option>
        {weekdayOptions.map((day, index) => <option key={day} value={index}>{day}</option>)}
        {end && <option value="7">Next Monday</option>}
      </select>
    </div>
  );
}

function WeeklyWindowFields({ disabled }: { disabled: boolean }) {
  return (
    <fieldset className="my-5 grid gap-4 rounded-lg border border-[var(--border)] p-4" disabled={disabled} aria-describedby="weekly-window-help">
      <legend className="px-2 text-sm font-bold">Weekly price window (UTC)</legend>
      <p id="weekly-window-help" className="help-text">Optional. Choose when this price is active each week. The start is included and the end is excluded. Use Next Monday at 00:00 to include all of Sunday; split a window that crosses that boundary.</p>
      <div className="inline-fields">
        <WeekdaySelect id="weekly_start_day" label="Start day" disabled={disabled} />
        <div className="field">
          <Label htmlFor="weekly_start_time">Start time</Label>
          <Input id="weekly_start_time" name="weekly_start_time" type="time" step="60" disabled={disabled} aria-describedby="weekly-window-help" />
        </div>
      </div>
      <div className="inline-fields">
        <WeekdaySelect id="weekly_end_day" label="End day" end disabled={disabled} />
        <div className="field">
          <Label htmlFor="weekly_end_time">End time</Label>
          <Input id="weekly_end_time" name="weekly_end_time" type="time" step="60" disabled={disabled} aria-describedby="weekly-window-help" />
        </div>
      </div>
    </fieldset>
  );
}

export function PriceSection({ prices, outbox, cursor, preview, busy, onLoadMore, onSubmit, onCancel }: PriceProps) {
  return (
    <section className="section-block">
      <div className="section-heading">
        <div><p className="context">Price provenance</p><h2>Effective prices</h2></div>
        <small>{(outbox?.pending_events ?? 0) + (outbox?.reserved_events ?? 0)} events pending or reserved</small>
      </div>
      <div className="resource-list">
        {prices.map((price) => <PriceCard price={price} key={price.id} />)}
      </div>
      {cursor && <Button className="section-block" variant="outline" disabled={busy} onClick={onLoadMore}>Load more prices</Button>}
      <Card className="panel section-block">
        <h2>Add price version</h2>
        <form onSubmit={onSubmit}>
          <div className="inline-fields"><Field id="price_connection" name="connection_id" label="Connection ID" required disabled={!!preview} /><Field id="price_model" name="model_id" label="Model ID" required disabled={!!preview} /></div>
          <div className="inline-fields">
            <Field id="input_price" label="Input USD / million" required disabled={!!preview} />
            <div className="field">
              <Label htmlFor="cache_read_price">Cache-read USD / million</Label>
              <Input id="cache_read_price" name="cache_read_price" disabled={!!preview} aria-describedby="cache-read-price-help" />
              <small id="cache-read-price-help" className="help-text">Optional. Cached reads remain unpriced when this is blank.</small>
            </div>
          </div>
          <Field id="output_price" label="Output USD / million" required disabled={!!preview} />
          <Field id="source" label="Source" required disabled={!!preview} />
          <div className="inline-fields"><Field id="effective_from" label="Effective from" type="datetime-local" required disabled={!!preview} /><Field id="effective_to" label="Effective to" type="datetime-local" disabled={!!preview} /></div>
          <WeeklyWindowFields disabled={!!preview} />
          {preview && <p className="form-success" role="status">Confirm {preview.connection_id} / {preview.model_id}: ${preview.input_usd_per_million} input, {preview.cache_read_usd_per_million === null ? "cache reads unpriced" : `$${preview.cache_read_usd_per_million} cache read`}, ${preview.output_usd_per_million} output · {formatWeeklyPriceWindow(preview.weekly_start_minute_utc, preview.weekly_end_minute_utc)} · {preview.source} · from {new Date(preview.effective_from).toLocaleString()} to {preview.effective_to ? new Date(preview.effective_to).toLocaleString() : "current"}?</p>}
          <div className="row-actions"><Button disabled={busy}>{preview ? "Confirm price" : "Preview price"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div>
        </form>
      </Card>
    </section>
  );
}
