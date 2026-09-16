import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function ModelField({ id, label, type = "text", defaultValue, required = false }: { id: string; label: string; type?: string; defaultValue?: string; required?: boolean }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} type={type} defaultValue={defaultValue} required={required} /></div>;
}
