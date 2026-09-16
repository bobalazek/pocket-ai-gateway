import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function ModelsSearch({ search, busy, onSearch }: { search: string; busy: boolean; onSearch: (value: string) => void }) {
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onSearch(String(new FormData(event.currentTarget).get("q") ?? ""));
  }

  return (
    <form className="filter-grid" key={search} onSubmit={submit}>
      <div className="field">
        <Label htmlFor="model-search">Search models</Label>
        <Input id="model-search" name="q" defaultValue={search} maxLength={100} placeholder="ID, name, provider, or upstream model" />
      </div>
      <Button disabled={busy}>Search</Button>
      {search && <Button type="button" variant="outline" disabled={busy} onClick={() => onSearch("")}>Clear</Button>}
    </form>
  );
}
