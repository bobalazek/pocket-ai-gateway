import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { RequestFilters } from "@/features/requests/types/requests.types";
import { localDatetime } from "@/features/usage/utils/usage.utils";

const textFilters = ["user_id", "key_id", "model_id"] as const;

export function RequestFilterForm({ filters, onSubmit }: { filters: RequestFilters; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return (
    <Card className="panel request-filters">
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
        <details className="request-advanced" open={Boolean(filters.connection_id || filters.operation || filters.state || filters.from || filters.to)}>
          <summary>Advanced filters</summary>
          <div className="filter-grid">
            <div className="field"><Label htmlFor="connection_id">Provider connection ID</Label><Input id="connection_id" name="connection_id" defaultValue={filters.connection_id} /></div>
            <div className="field"><Label htmlFor="operation">Operation</Label><Input id="operation" name="operation" defaultValue={filters.operation} /></div>
            <div className="field">
              <Label htmlFor="state">Outcome</Label>
              <select id="state" name="state" className="select" defaultValue={filters.state ?? ""}>
                <option value="">All</option>
                <option value="reserved">Reserved</option><option value="in_progress">In progress</option><option value="succeeded">Succeeded</option><option value="failed">Failed</option><option value="cancelled">Cancelled</option><option value="interrupted_unknown">Interrupted</option>
              </select>
            </div>
            <div className="field"><Label htmlFor="from">From</Label><Input id="from" name="from" type="datetime-local" defaultValue={localDatetime(filters.from)} /></div>
            <div className="field"><Label htmlFor="to">To</Label><Input id="to" name="to" type="datetime-local" defaultValue={localDatetime(filters.to)} /></div>
          </div>
        </details>
      </form>
    </Card>
  );
}
