"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { AppShell } from "@/components/app-shell";
import { useGatewayUser } from "@/components/setup-gate";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type ManagedUser } from "@/lib/api-client";

const scopes = ["chat:generate", "responses:generate", "embeddings:generate", "moderations:classify", "images:generate", "images:edit", "images:variation", "audio:speech", "audio:transcribe", "audio:translate", "models:read", "tokens:count"];
const list = (value: FormDataEntryValue | null) => String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);

export default function UsersPage() {
  const current = useGatewayUser();
  const [users, setUsers] = useState<ManagedUser[]>([]);
	const [nextCursor, setNextCursor] = useState("");
	const [oneTimeCode, setOneTimeCode] = useState<{ value: string; user: string; userID: string; purpose: string } | null>(null);
  const [error, setError] = useState("");
	const [issuing, setIssuing] = useState(false);
	const [loadingPage, setLoadingPage] = useState(false);
	const issuingRef = useRef(false);
	const loadingRef = useRef(false);
	const pageRequestRef = useRef(0);

	async function loadPage(cursor = "", push = false, supersede = false) {
		if (loadingRef.current && !supersede) return;
		const request = ++pageRequestRef.current;
		loadingRef.current = true; setLoadingPage(true);
		try { const page = await gatewayAPI.users(cursor); if (request !== pageRequestRef.current) return; setUsers(page.data); setNextCursor(page.next_cursor); if (push) window.history.pushState({}, "", cursor ? `?cursor=${encodeURIComponent(cursor)}` : window.location.pathname); }
		catch (failure) { if (request === pageRequestRef.current) showError(failure); }
		finally { if (request === pageRequestRef.current) { loadingRef.current = false; setLoadingPage(false); } }
	}
	async function refresh() { await loadPage(new URLSearchParams(window.location.search).get("cursor") ?? ""); }
	useEffect(() => { const reload = () => void loadPage(new URLSearchParams(window.location.search).get("cursor") ?? "", false, true); void reload(); window.addEventListener("popstate", reload); return () => window.removeEventListener("popstate", reload); }, []);
  function showError(failure: unknown) { setError(failure instanceof GatewayAPIError ? failure.message : "Request failed"); }

  async function create(event: FormEvent<HTMLFormElement>) {
	event.preventDefault(); setError(""); setOneTimeCode(null);
		if (issuingRef.current) return;
		issuingRef.current = true;
	setIssuing(true);
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    try {
      const result = await gatewayAPI.createUser({ email: String(form.get("email")), display_name: String(form.get("display_name")), role: String(form.get("role")) as "admin" | "member", grants: { unrestricted: false, scopes: form.getAll("scopes").map(String), model_patterns: list(form.get("models")), connection_ids: list(form.get("connections")) } });
		setOneTimeCode({ value: result.activation_code, user: result.user.display_name, userID: result.user.id, purpose: "activation" }); formElement.reset(); await refresh();
	} catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }

  async function setStatus(user: ManagedUser, status: "active" | "suspended") {
    try { await gatewayAPI.updateUser(user.id, user.revision, { status }); await refresh(); } catch (failure) { showError(failure); }
  }

  async function issueCode(user: ManagedUser, purpose: "activation" | "recovery") {
	if (!window.confirm(`${purpose === "recovery" ? "Issue a recovery code and sign out" : "Replace the activation code for"} ${user.display_name}?`)) return;
	setOneTimeCode(null);
		if (issuingRef.current) return;
		issuingRef.current = true;
	setIssuing(true);
	try { const result = await gatewayAPI.userCode(user.id, purpose); setOneTimeCode({ value: result.code, user: user.display_name, userID: user.id, purpose }); await refresh(); } catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }

  async function transfer(user: ManagedUser) {
    if (!window.confirm(`Transfer ownership to ${user.display_name}? Both accounts will be signed out.`)) return;
    try { await gatewayAPI.transferOwner(user.id, user.revision); window.location.replace("/_/login/"); } catch (failure) { showError(failure); }
  }

  if (current?.role === "member") return <AppShell active="Users"><main id="main-content" className="content"><p className="context">Access denied</p><h1>Administrator access required.</h1></main></AppShell>;
	const permittedScopes = current?.grants.unrestricted ? scopes : current?.grants.scopes ?? [];

  return (
    <AppShell active="Users">
      <main id="main-content" className="content management-page">
        <header className="page-header"><div><p className="context">Identity</p><h1>Users</h1><p className="lede">Invite members and, as owner, administrators. Codes appear once.</p></div></header>
        {error && <p className="form-error" role="alert">{error}</p>}
		{oneTimeCode && <Card className="secret-card" role="status"><div><strong>Copy {oneTimeCode.user}&apos;s {oneTimeCode.purpose} code now</strong><code>{oneTimeCode.value}</code><small>{oneTimeCode.userID} · never shown again · expires in 24 hours</small></div><Button variant="outline" onClick={() => navigator.clipboard.writeText(oneTimeCode.value)}>Copy</Button></Card>}
        <Card className="panel compact-panel"><h2>Add user</h2><form onSubmit={create}>
          <div className="inline-form"><div className="field"><Label htmlFor="display_name">Name</Label><Input id="display_name" name="display_name" required /></div>
          <div className="field"><Label htmlFor="email">Email</Label><Input id="email" name="email" type="email" required /></div>
          <div className="field"><Label htmlFor="role">Role</Label><select id="role" name="role" className="select" defaultValue="member"><option value="member">Member</option>{current?.role === "owner" && <option value="admin">Administrator</option>}</select></div></div>
			<fieldset className="scope-grid"><legend>Maximum operation scopes</legend>{permittedScopes.map((scope) => <label key={scope}><input type="checkbox" name="scopes" value={scope} /> <span>{scope}</span></label>)}</fieldset>
			<small>You can assign only scopes from your own ceiling. Allowed models: {current?.grants.unrestricted ? "any" : current?.grants.model_patterns.join(", ") || "none"}. Allowed connections: {current?.grants.unrestricted ? "any" : current?.grants.connection_ids.join(", ") || "none"}.</small>
          <div className="inline-fields"><div className="field"><Label htmlFor="models">Model patterns</Label><Input id="models" name="models" placeholder="gpt-*" /></div><div className="field"><Label htmlFor="connections">Connection IDs</Label><Input id="connections" name="connections" placeholder="conn_primary" /></div></div>
			<Button type="submit" disabled={issuing}>{issuing ? "Working…" : "Create user"}</Button>
        </form></Card>
        <section className="section-block"><div className="section-heading"><div><p className="context">Directory</p><h2>{users.length} users</h2></div></div>
          <div className="resource-list">{users.map((user) => <Card className="resource-row stack" key={user.id}>
            <div className="resource-row-main"><div className="resource-identity"><span className="account-avatar light" aria-hidden="true">{user.display_name.charAt(0).toUpperCase()}</span><span><strong>{user.display_name}{user.id === current?.id ? " (you)" : ""}</strong><small>{user.email} · {user.role} · {user.status.replace("_", " ")} · {user.grants.unrestricted ? "unrestricted" : `${user.grants.scopes.length} scopes`}</small></span></div>
            {user.id !== current?.id && user.role !== "owner" && <div className="row-actions">
				{user.status === "pending_activation" ? <Button variant="outline" disabled={issuing} onClick={() => issueCode(user, "activation")}>New activation code</Button> : <Button variant="outline" disabled={issuing} onClick={() => issueCode(user, "recovery")}>Recovery code</Button>}
              {user.status === "active" ? <Button variant="outline" onClick={() => setStatus(user, "suspended")}>Suspend</Button> : user.status === "suspended" && <Button variant="outline" onClick={() => setStatus(user, "active")}>Reactivate</Button>}
              {current?.role === "owner" && user.role === "admin" && user.status === "active" && <Button variant="outline" onClick={() => transfer(user)}>Transfer ownership</Button>}
            </div>}</div>
			{user.id !== current?.id && user.role !== "owner" && <GrantEditor user={user} allowedScopes={permittedScopes} onSaved={refresh} onError={showError} />}
			</Card>)}</div>
			{nextCursor && <Button variant="outline" disabled={loadingPage} onClick={() => loadPage(nextCursor, true)}>{loadingPage ? "Loading…" : "Next page"}</Button>}
        </section>
      </main>
    </AppShell>
  );
}

function GrantEditor({ user, allowedScopes, onSaved, onError }: { user: ManagedUser; allowedScopes: string[]; onSaved: () => Promise<void>; onError: (error: unknown) => void }) {
	const modelID = `models-${user.id}`;
	const connectionID = `connections-${user.id}`;
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      await gatewayAPI.updateUserGrants(user.id, user.revision, { unrestricted: false, scopes: form.getAll("scopes").map(String), model_patterns: list(form.get("models")), connection_ids: list(form.get("connections")) });
      await onSaved();
    } catch (failure) { onError(failure); }
  }
  return <details className="grant-editor"><summary>Edit inference grants</summary><form onSubmit={save}>
		<fieldset className="scope-grid"><legend>Maximum operation scopes</legend>{allowedScopes.map((scope) => <label key={scope}><input type="checkbox" name="scopes" value={scope} defaultChecked={user.grants.scopes.includes(scope)} /> <span>{scope}</span></label>)}</fieldset>
		<div className="inline-fields"><div className="field"><Label htmlFor={modelID}>Model patterns</Label><Input id={modelID} name="models" defaultValue={user.grants.model_patterns.join(", ")} /></div><div className="field"><Label htmlFor={connectionID}>Connection IDs</Label><Input id={connectionID} name="connections" defaultValue={user.grants.connection_ids.join(", ")} /></div></div>
    <Button type="submit">Save grants</Button>
  </form></details>;
}
