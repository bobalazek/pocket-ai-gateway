import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function Field({ id, name, label, type = "text", required = false, disabled = false, defaultValue }: { id: string; name?: string; label: string; type?: string; required?: boolean; disabled?: boolean; defaultValue?: string }) {
  return (
    <div className="field">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} name={name ?? id} type={type} required={required} disabled={disabled} defaultValue={defaultValue} />
    </div>
  );
}

export function Metric({ label, value }: { label: string; value: string }) {
  return <Card><small>{label}</small><strong>{value}</strong></Card>;
}
