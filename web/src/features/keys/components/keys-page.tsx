"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { InferenceScopeHelp } from "@/features/auth/components/inference-scope-help";
import type { GatewayKey } from "@/features/keys/types/keys.types";
import { useKeys } from "@/features/keys/hooks/use-keys";
import { effectiveKeyState } from "@/features/keys/utils/key.utils";

export default function KeysPage() {
	const { current, keys, nextCursor, secret, error, issuing, loadingPage, permittedScopes, create, rotate, revoke, toggle, loadPage } = useKeys();

  return (
    <AppShell active="API keys">
      <main id="main-content" className="content management-page">
		<header className="page-header"><div><p className="context">Access</p><h1>API keys</h1><p className="lede">Every key starts with explicit operation, model, and connection grants. Your ceiling is {current?.grants.unrestricted ? "unrestricted" : `${permittedScopes.length} scopes, ${current?.grants.model_patterns.length ?? 0} model patterns, and ${current?.grants.connection_ids.length ?? 0} connections`}.</p></div></header>
        {error && <p className="form-error" role="alert">{error}</p>}
		{secret && <Card className="secret-card" role="status"><div><strong>Copy the {secret.label} secret now</strong><code>{secret.value}</code><small>{secret.keyID} · only its verifier is stored</small></div><div className="row-actions"><Button variant="outline" onClick={() => navigator.clipboard.writeText(secret.value)}>Copy secret</Button><Button variant="outline" onClick={() => navigator.clipboard.writeText(`OPENAI_API_KEY=${secret.value}\nOPENAI_BASE_URL=${window.location.origin}/api/openai/v1`)}>Copy client config</Button></div></Card>}
        <Card className="panel"><h2>Create key</h2><form onSubmit={create}>
          <div className="field"><Label htmlFor="label">Label</Label><Input id="label" name="label" placeholder="Production app" required /></div>
			<fieldset className="scope-grid"><legend>Operation scopes</legend>{permittedScopes.map((scope) => <label key={scope}><input type="checkbox" name="scopes" value={scope} /> <span>{scope}</span></label>)}</fieldset>
			<InferenceScopeHelp />
			<small>Your key can only narrow your current user grants. Allowed models: {current?.grants.unrestricted ? "any" : current?.grants.model_patterns.join(", ") || "none"}. Allowed connections: {current?.grants.unrestricted ? "any" : current?.grants.connection_ids.join(", ") || "none"}.</small>
          <div className="inline-fields"><div className="field"><Label htmlFor="models">Model patterns</Label><Input id="models" name="models" placeholder="gpt-*, claude-*" /><small>Comma-separated. Empty denies every model.</small></div><div className="field"><Label htmlFor="connections">Connection IDs</Label><Input id="connections" name="connections" placeholder="conn_primary" /><small>Comma-separated. Empty denies every connection.</small></div></div>
          <div className="field"><Label htmlFor="expires_at">Expires</Label><Input id="expires_at" name="expires_at" type="datetime-local" /><small>Optional. Times use this browser&apos;s local timezone.</small></div>
			<Button type="submit" disabled={issuing}>{issuing ? "Creating…" : "Create key"}</Button>
        </form></Card>
        <section className="section-block"><div className="section-heading"><div><p className="context">Credentials</p><h2>{keys.length} keys</h2></div></div>
			<div className="resource-list">{keys.map((key) => { const effectiveState = effectiveKeyState(key); return <Card className="resource-row" key={key.id}><div><strong>{key.label}</strong><small>{effectiveState} · {key.scopes.length} scopes · {key.model_patterns.length} model grants · {key.connection_ids.length} connections{key.expires_at ? ` · expires ${new Date(key.expires_at).toLocaleString()}` : ""}</small></div>{key.state !== "revoked" && <div className="row-actions">{effectiveState !== "expired" && <Button variant="outline" onClick={() => toggle(key)}>{key.state === "active" ? "Disable" : "Enable"}</Button>}{effectiveState === "active" && <Button variant="outline" disabled={issuing} onClick={() => rotate(key)}>Rotate</Button>}<Button variant="outline" onClick={() => revoke(key)}>Revoke</Button></div>}</Card>; })}</div>
			{nextCursor && <Button variant="outline" disabled={loadingPage} onClick={() => loadPage(nextCursor, true)}>{loadingPage ? "Loading…" : "Next page"}</Button>}
        </section>
      </main>
    </AppShell>
  );
}
