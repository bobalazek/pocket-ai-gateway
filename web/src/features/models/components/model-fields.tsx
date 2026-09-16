import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { RouteStrategy } from "@/features/models/types/models.types";

export const routeStrategies: { value: RouteStrategy; label: string }[] = [
  { value: "fixed", label: "Fixed target" },
  { value: "ordered_fallback", label: "Ordered fallback" },
  { value: "weighted", label: "Weighted" },
  { value: "lowest_cost", label: "Lowest estimated cost" },
  { value: "lowest_latency", label: "Lowest observed latency" },
];

export function ModelField({ id, label, type = "text", defaultValue, required = false }: { id: string; label: string; type?: string; defaultValue?: string; required?: boolean }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} type={type} defaultValue={defaultValue} required={required} /></div>;
}
