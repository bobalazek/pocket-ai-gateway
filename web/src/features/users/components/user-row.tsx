"use client";

import type { FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { InferenceScopeHelp } from "@/features/auth/components/inference-scope-help";
import { useGrantEditor } from "@/features/users/hooks/use-users";
import type { ManagedUser } from "@/features/users/types/users.types";

type Props = {
  user: ManagedUser;
  currentID?: string;
  currentRole?: string;
  issuing: boolean;
  allowedScopes: string[];
  onStatus: (user: ManagedUser, status: "active" | "suspended") => Promise<void>;
  onCode: (user: ManagedUser, purpose: "activation" | "recovery") => Promise<void>;
  onTransfer: (user: ManagedUser) => Promise<void>;
  onSaved: () => Promise<void>;
  onError: (error: unknown) => void;
};

export function UserRow({ user, currentID, currentRole, issuing, allowedScopes, onStatus, onCode, onTransfer, onSaved, onError }: Props) {
  const editable = user.id !== currentID && user.role !== "owner";
  return <Card className="resource-row stack"><div className="resource-row-main"><div className="resource-identity"><span className="account-avatar light" aria-hidden="true">{user.display_name.charAt(0).toUpperCase()}</span><span><strong>{user.display_name}{user.id === currentID ? " (you)" : ""}</strong><small>{user.email} · {user.role} · {user.status.replace("_", " ")} · {user.grants.unrestricted ? "unrestricted" : `${user.grants.scopes.length} scopes`}</small></span></div>
    {editable && <div className="row-actions">{user.status === "pending_activation" ? <Button variant="outline" disabled={issuing} onClick={() => onCode(user, "activation")}>New activation code</Button> : <Button variant="outline" disabled={issuing} onClick={() => onCode(user, "recovery")}>Recovery code</Button>}{user.status === "active" ? <Button variant="outline" onClick={() => onStatus(user, "suspended")}>Suspend</Button> : user.status === "suspended" && <Button variant="outline" onClick={() => onStatus(user, "active")}>Reactivate</Button>}{currentRole === "owner" && user.role === "admin" && user.status === "active" && <Button variant="outline" onClick={() => onTransfer(user)}>Transfer ownership</Button>}</div>}
  </div>{editable && <GrantEditor user={user} allowedScopes={allowedScopes} onSaved={onSaved} onError={onError}/>}</Card>;
}

function GrantEditor({ user, allowedScopes, onSaved, onError }: { user: ManagedUser; allowedScopes: string[]; onSaved: () => Promise<void>; onError: (error: unknown) => void }) {
  const save = useGrantEditor(user, onSaved, onError);
  return <details className="grant-editor"><summary>Edit inference grants</summary><form onSubmit={save}><fieldset className="scope-grid"><legend>Maximum operation scopes</legend>{allowedScopes.map((scope) => <label key={scope}><input type="checkbox" name="scopes" value={scope} defaultChecked={user.grants.scopes.includes(scope)} /> <span>{scope}</span></label>)}</fieldset><InferenceScopeHelp /><GrantFields user={user}/><Button type="submit">Save grants</Button></form></details>;
}

function GrantFields({ user }: { user: ManagedUser }) {
  return <div className="inline-fields"><div className="field"><Label htmlFor={`models-${user.id}`}>Model patterns</Label><Input id={`models-${user.id}`} name="models" defaultValue={user.grants.model_patterns.join(", ")} /></div><div className="field"><Label htmlFor={`connections-${user.id}`}>Connection IDs</Label><Input id={`connections-${user.id}`} name="connections" defaultValue={user.grants.connection_ids.join(", ")} /></div></div>;
}
