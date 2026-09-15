import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Field } from "@/features/usage/components/usage-fields";
import type { useUsage } from "@/features/usage/hooks/use-usage";

type UsageModel = ReturnType<typeof useUsage>;

export function UsageLimits({ model }: { model: UsageModel }) {
  return (
    <>
      <section className="section-block">
        <h2>Limit policies</h2>
        <div className="resource-list">
          {model.policies.map((policy) => (
            <Card className="resource-row" key={policy.id}>
              <div><strong>{policy.metric.replaceAll("_", " ")} · {policy.limit_usd ? `$${policy.limit_usd}` : policy.limit_units.toLocaleString()}</strong><small>{policy.scope_kind}{policy.scope_id ? ` ${policy.scope_id}` : ""} · {policy.algorithm}{policy.period ? ` / ${policy.period}` : ""} · {policy.enabled ? "enforcing" : "disabled"}</small></div>
              {model.canManage && (policy.scope_kind !== "instance" || model.isOwner) && (
                <div className="row-actions">
                  <form className="limit-edit" onSubmit={(event) => { event.preventDefault(); void model.changePolicy(policy, String(new FormData(event.currentTarget).get("limit")), policy.enabled); }}>
                    <Input aria-label={`New ${policy.metric} limit`} name="limit" defaultValue={policy.limit_usd ?? policy.limit_units} />
                    <Button variant="outline" disabled={model.busy}>Save</Button>
                  </form>
                  <Button variant="outline" disabled={model.busy} onClick={() => model.changePolicy(policy, policy.limit_usd ?? String(policy.limit_units), !policy.enabled)}>{policy.enabled ? "Disable" : "Enable"}</Button>
                </div>
              )}
            </Card>
          ))}
        </div>
        {model.policyCursor && <Button className="section-block" variant="outline" disabled={model.busy} onClick={() => model.loadMore("policies")}>Load more policies</Button>}
      </section>
      <Card className="panel">
        <h2>Inspect effective limits</h2>
        <form onSubmit={model.inspectLimits}><div className="inline-fields"><Field id="limits_key" name="key_id" label="API key ID" required /><Field id="limits_connection" name="connection_id" label="Connection ID" /></div><Button disabled={model.busy}>Inspect</Button></form>
        {model.limits.length > 0 && <div className="resource-list section-block">{model.limits.map((limit) => <div className="resource-row" key={limit.policy_id}><div><strong>{limit.scope_kind} {limit.metric} · cap {limit.limit_usd ? `$${limit.limit_usd}` : limit.limit_units.toLocaleString()}</strong><small>{limit.consumed_usd ? `$${limit.consumed_usd}` : limit.consumed_units.toLocaleString()} consumed · {limit.reserved_usd ? `$${limit.reserved_usd}` : limit.reserved_units.toLocaleString()} reserved · {limit.remaining_usd ? `$${limit.remaining_usd}` : limit.remaining_units.toLocaleString()} remaining · {limit.algorithm}{limit.period ? ` / ${limit.period}` : ""}{limit.resets_at ? ` · resets ${new Date(limit.resets_at).toLocaleString()}` : ""}</small></div></div>)}</div>}
      </Card>
    </>
  );
}
