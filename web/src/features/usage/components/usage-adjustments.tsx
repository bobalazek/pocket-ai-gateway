import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Field } from "@/features/usage/components/usage-fields";
import type { AdjustmentPreview, ReconciliationPreview, RepricePreview, UnresolvedAttempt } from "@/features/usage/types/usage.types";

export function RepriceForm({ preview, busy, onPreview, onApply, onCancel }: { preview: RepricePreview | null; busy: boolean; onPreview: (event: FormEvent<HTMLFormElement>) => void; onApply: (event: FormEvent<HTMLFormElement>) => void; onCancel: () => void }) {
  return (
    <Card className="panel">
      <h2>Historical repricing</h2>
      <form onSubmit={preview ? onApply : onPreview}>
        <div className="inline-fields"><Field id="reprice_connection" name="connection_id" label="Connection ID" required disabled={!!preview} /><Field id="reprice_model" name="model_id" label="Model ID" required disabled={!!preview} /></div>
        <div className="inline-fields"><Field id="reprice_from" name="from" label="From" type="datetime-local" required disabled={!!preview} /><Field id="reprice_to" name="to" label="To" type="datetime-local" required disabled={!!preview} /></div>
        {preview && <p className="form-success" role="status">{preview.text}</p>}
        <div className="row-actions"><Button disabled={busy}>{preview ? "Apply repricing" : "Preview repricing"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div>
      </form>
    </Card>
  );
}

export function AdjustmentForm({ preview, busy, onSubmit, onCancel }: { preview: AdjustmentPreview | null; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>) => void; onCancel: () => void }) {
  return (
    <Card className="panel">
      <h2>Cost adjustment</h2>
      <form onSubmit={onSubmit}>
        <Field id="attempt_id" label="Attempt ID" required disabled={!!preview} /><Field id="delta_usd" label="USD change" required disabled={!!preview} /><Field id="reason" label="Reason" required disabled={!!preview} />
        {preview && <p className="form-success" role="status">Apply {preview.delta_usd} USD to {preview.attempt_id}?</p>}
        <div className="row-actions"><Button disabled={busy}>{preview ? "Confirm adjustment" : "Preview adjustment"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div>
      </form>
    </Card>
  );
}

export function UnknownRow({ attempt, preview, canManage, busy, onSubmit, onCancel }: { attempt: UnresolvedAttempt; preview: ReconciliationPreview | null; canManage: boolean; busy: boolean; onSubmit: (event: FormEvent<HTMLFormElement>, attempt: UnresolvedAttempt) => void; onCancel: () => void }) {
  return (
    <Card className="panel">
      <strong>{attempt.model_id}</strong><small>{attempt.id} · {attempt.connection_id} · {new Date(attempt.started_at).toLocaleString()}</small>
      {canManage && <form className="section-block" onChange={() => preview && onCancel()} onSubmit={(event) => onSubmit(event, attempt)}>
        <div className="inline-fields"><Field id={`input-${attempt.id}`} name="input_tokens" label="Input tokens" type="number" required /><Field id={`output-${attempt.id}`} name="output_tokens" label="Output tokens" type="number" required /></div>
        <div className="inline-fields"><Field id={`cost-${attempt.id}`} name="cost_usd" label="Known cost USD (optional)" /><div className="field"><Label htmlFor={`status-${attempt.id}`}>Source</Label><select id={`status-${attempt.id}`} className="select" name="usage_status"><option value="provider_reported">Provider reported</option><option value="estimated">Estimated</option></select></div></div>
        <Field id={`reason-${attempt.id}`} name="reason" label="Reason" required />
        {preview && <p className="form-success" role="status">Confirm {preview.input_tokens + preview.output_tokens} tokens{preview.cost_usd ? ` and $${preview.cost_usd}` : ""}?</p>}
        <div className="row-actions"><Button disabled={busy}>{preview ? "Confirm reconciliation" : "Preview reconciliation"}</Button>{preview && <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>}</div>
      </form>}
    </Card>
  );
}
