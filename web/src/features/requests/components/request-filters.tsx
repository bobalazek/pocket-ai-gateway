import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { RequestFilters } from "@/features/requests/types/requests.types";

const textFilters = ["user_id", "key_id", "model_id"] as const;

export function RequestFilterForm({ filters, onSubmit }: { filters: RequestFilters; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return (
    <Card className="panel">
      <h2>Filter requests</h2>
      <form className="filter-grid" key={JSON.stringify(filters)} onSubmit={onSubmit}>
        {textFilters.map((id) => (
          <div className="field" key={id}>
            <Label htmlFor={id}>{id.replaceAll("_", " ")}</Label>
            <Input id={id} name={id} defaultValue={filters[id]} />
          </div>
        ))}
        <div className="field">
          <Label htmlFor="dialect">Protocol</Label>
          <select id="dialect" name="dialect" className="select" defaultValue={filters.dialect ?? ""}>
            <option value="">All</option>
            <option>openai</option><option>responses</option><option>anthropic</option><option>gemini</option>
          </select>
        </div>
        <Button>Apply</Button>
      </form>
    </Card>
  );
}
