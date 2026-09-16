import type { GatewayInferenceScope } from "@/features/auth/types/auth.types";

export function InferenceScopeOptions({ scopes, selected = [] }: { scopes: GatewayInferenceScope[]; selected?: string[] }) {
  return scopes.map((scope) => (
    <label key={scope.id}>
      <input type="checkbox" name="scopes" value={scope.id} defaultChecked={selected.includes(scope.id)} />
      <span>{scope.id}{scope.policy && <small>{scope.policy}</small>}</span>
    </label>
  ));
}
