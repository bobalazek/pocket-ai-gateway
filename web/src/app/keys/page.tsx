"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { AppShell } from "@/components/app-shell";
import { useGatewayUser } from "@/components/setup-gate";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { effectiveKeyState, GatewayAPIError, gatewayAPI, type GatewayKey } from "@/lib/api-client";

const scopes = ["chat:generate", "responses:generate", "embeddings:generate", "moderations:classify", "images:generate", "audio:speech", "audio:transcribe", "audio:translate", "models:read", "tokens:count"];
const list = (value: FormDataEntryValue | null) => String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);

export default function KeysPage() {
	const current = useGatewayUser();
  const [keys, setKeys] = useState<GatewayKey[]>([]);
	const [nextCursor, setNextCursor] = useState("");
	const [secret, setSecret] = useState<{ value: string; label: string; keyID: string } | null>(null);
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
		try { const page = await gatewayAPI.keys(cursor); if (request !== pageRequestRef.current) return; setKeys(page.data); setNextCursor(page.next_cursor); if (push) window.history.pushState({}, "", cursor ? `?cursor=${encodeURIComponent(cursor)}` : window.location.pathname); }
		catch (failure) { if (request === pageRequestRef.current) showError(failure); }
		finally { if (request === pageRequestRef.current) { loadingRef.current = false; setLoadingPage(false); } }
	}
	async function refresh() { await loadPage(new URLSearchParams(window.location.search).get("cursor") ?? ""); }
	useEffect(() => { const reload = () => void loadPage(new URLSearchParams(window.location.search).get("cursor") ?? "", false, true); void reload(); window.addEventListener("popstate", reload); return () => window.removeEventListener("popstate", reload); }, []);
  function showError(failure: unknown) { setError(failure instanceof GatewayAPIError ? failure.message : "Request failed"); }

  async function create(event: FormEvent<HTMLFormElement>) {
	event.preventDefault(); setError(""); setSecret(null);
		if (issuingRef.current) return;
		issuingRef.current = true;
	setIssuing(true);
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    try {
      const expiry = String(form.get("expires_at") ?? "");
      const result = await gatewayAPI.createKey({ label: String(form.get("label")), scopes: form.getAll("scopes").map(String), model_patterns: list(form.get("models")), connection_ids: list(form.get("connections")), expires_at: expiry ? new Date(expiry).toISOString() : "" });
		setSecret({ value: result.secret, label: result.key.label, keyID: result.key.id }); formElement.reset(); await refresh();
	} catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }

	async function rotate(key: GatewayKey) { if (issuingRef.current || !window.confirm(`Rotate ${key.label}? Its current secret will stop working immediately.`)) return; issuingRef.current = true; setSecret(null); setIssuing(true); try { const result = await gatewayAPI.rotateKey(key.id, key.revision); setSecret({ value: result.secret, label: result.key.label, keyID: result.key.id }); await refresh(); } catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); } }
	async function revoke(key: GatewayKey) { if (!window.confirm(`Revoke ${key.label}? This cannot be undone.`)) return; setSecret(null); try { await gatewayAPI.revokeKey(key.id, key.revision); await refresh(); } catch (failure) { showError(failure); } }
  async function toggle(key: GatewayKey) { try { await gatewayAPI.updateKey(key.id, key.revision, { label: key.label, state: key.state === "active" ? "disabled" : "active", scopes: key.scopes, model_patterns: key.model_patterns, connection_ids: key.connection_ids, expires_at: key.expires_at ?? "" }); await refresh(); } catch (failure) { showError(failure); } }
	const permittedScopes = current?.grants.unrestricted ? scopes : current?.grants.scopes ?? [];

  return (
    <AppShell active="API keys">
      <main id="main-content" className="content management-page">
		<header className="page-header"><div><p className="context">Access</p><h1>API keys</h1><p className="lede">Every key starts with explicit operation, model, and connection grants. Your ceiling is {current?.grants.unrestricted ? "unrestricted" : `${permittedScopes.length} scopes, ${current?.grants.model_patterns.length ?? 0} model patterns, and ${current?.grants.connection_ids.length ?? 0} connections`}.</p></div></header>
        {error && <p className="form-error" role="alert">{error}</p>}
		{secret && <Card className="secret-card" role="status"><div><strong>Copy the {secret.label} secret now</strong><code>{secret.value}</code><small>{secret.keyID} · only its verifier is stored</small></div><div className="row-actions"><Button variant="outline" onClick={() => navigator.clipboard.writeText(secret.value)}>Copy secret</Button><Button variant="outline" onClick={() => navigator.clipboard.writeText(`OPENAI_API_KEY=${secret.value}\nOPENAI_BASE_URL=${window.location.origin}/api/openai/v1`)}>Copy client config</Button></div></Card>}
        <Card className="panel"><h2>Create key</h2><form onSubmit={create}>
          <div className="field"><Label htmlFor="label">Label</Label><Input id="label" name="label" placeholder="Production app" required /></div>
			<fieldset className="scope-grid"><legend>Operation scopes</legend>{permittedScopes.map((scope) => <label key={scope}><input type="checkbox" name="scopes" value={scope} /> <span>{scope}</span></label>)}</fieldset>
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
