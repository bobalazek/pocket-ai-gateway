"use client";


import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { WebSearchScopeHelp } from "@/features/auth/components/web-search-scope-help";
import { UserRow } from "@/features/users/components/user-row";
import { useUsers } from "@/features/users/hooks/use-users";

export default function UsersPage() {
  const { current, users, nextCursor, oneTimeCode, error, issuing, loadingPage, permittedScopes, create, setStatus, issueCode, transfer, refresh, showError, loadPage } = useUsers();
  if (current?.role === "member") return <AppShell active="Users"><main id="main-content" className="content"><p className="context">Access denied</p><h1>Administrator access required.</h1></main></AppShell>;

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
			<WebSearchScopeHelp />
			<small>You can assign only scopes from your own ceiling. Allowed models: {current?.grants.unrestricted ? "any" : current?.grants.model_patterns.join(", ") || "none"}. Allowed connections: {current?.grants.unrestricted ? "any" : current?.grants.connection_ids.join(", ") || "none"}.</small>
          <div className="inline-fields"><div className="field"><Label htmlFor="models">Model patterns</Label><Input id="models" name="models" placeholder="gpt-*" /></div><div className="field"><Label htmlFor="connections">Connection IDs</Label><Input id="connections" name="connections" placeholder="conn_primary" /></div></div>
			<Button type="submit" disabled={issuing}>{issuing ? "Working…" : "Create user"}</Button>
        </form></Card>
        <section className="section-block"><div className="section-heading"><div><p className="context">Directory</p><h2>{users.length} users</h2></div></div>
	          <div className="resource-list">{users.map((user) => <UserRow key={user.id} user={user} currentID={current?.id} currentRole={current?.role} issuing={issuing} allowedScopes={permittedScopes} onStatus={setStatus} onCode={issueCode} onTransfer={transfer} onSaved={refresh} onError={showError}/>)}</div>
			{nextCursor && <Button variant="outline" disabled={loadingPage} onClick={() => loadPage(nextCursor, true)}>{loadingPage ? "Loading…" : "Next page"}</Button>}
        </section>
      </main>
    </AppShell>
  );
}
