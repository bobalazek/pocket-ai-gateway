"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { useGatewayInferenceScopes, useGatewayUser } from "@/components/setup-gate";
import type { ManagedUser, OneTimeCode } from "@/features/users/types/users.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export const splitList = (value: FormDataEntryValue | null) => String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);

export function useUsers() {
  const current = useGatewayUser();
  const inferenceScopes = useGatewayInferenceScopes();
  const [users, setUsers] = useState<ManagedUser[]>([]);
	const [nextCursor, setNextCursor] = useState("");
	const [oneTimeCode, setOneTimeCode] = useState<OneTimeCode | null>(null);
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
		try { const page = await pocketAIGatewayAdmin.users.list(cursor); if (request !== pageRequestRef.current) return; setUsers(page.data); setNextCursor(page.next_cursor); if (push) window.history.pushState({}, "", cursor ? `?cursor=${encodeURIComponent(cursor)}` : window.location.pathname); }
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
      const result = await pocketAIGatewayAdmin.users.create({ email: String(form.get("email")), display_name: String(form.get("display_name")), role: String(form.get("role")) as "admin" | "member", grants: { unrestricted: false, scopes: form.getAll("scopes").map(String), model_patterns: splitList(form.get("models")), connection_ids: splitList(form.get("connections")) } });
		setOneTimeCode({ value: result.activation_code, user: result.user.display_name, userID: result.user.id, purpose: "activation" }); formElement.reset(); await refresh();
	} catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }

  async function setStatus(user: ManagedUser, status: "active" | "suspended") {
    try { await pocketAIGatewayAdmin.users.update(user.id, user.revision, { status }); await refresh(); } catch (failure) { showError(failure); }
  }

  async function issueCode(user: ManagedUser, purpose: "activation" | "recovery") {
	if (!window.confirm(`${purpose === "recovery" ? "Issue a recovery code and sign out" : "Replace the activation code for"} ${user.display_name}?`)) return;
	setOneTimeCode(null);
		if (issuingRef.current) return;
		issuingRef.current = true;
	setIssuing(true);
	try { const result = await pocketAIGatewayAdmin.users.issueCode(user.id, purpose); setOneTimeCode({ value: result.code, user: user.display_name, userID: user.id, purpose }); await refresh(); } catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }

  async function transfer(user: ManagedUser) {
    if (!window.confirm(`Transfer ownership to ${user.display_name}? Both accounts will be signed out.`)) return;
    try { await pocketAIGatewayAdmin.users.transferOwner(user.id, user.revision); window.location.replace("/_/login/"); } catch (failure) { showError(failure); }
  }

  const permittedScopes = inferenceScopes.filter((scope) => current?.grants.unrestricted || current?.grants.scopes.includes(scope.id));
  return { current, users, nextCursor, oneTimeCode, error, issuing, loadingPage, permittedScopes, create, setStatus, issueCode, transfer, refresh, showError, loadPage };
}

export function useGrantEditor(user: ManagedUser, onSaved: () => Promise<void>, onError: (error: unknown) => void) {
  return async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    try { await pocketAIGatewayAdmin.users.updateGrants(user.id, user.revision, { unrestricted: false, scopes: form.getAll("scopes").map(String), model_patterns: splitList(form.get("models")), connection_ids: splitList(form.get("connections")) }); await onSaved(); }
    catch (failure) { onError(failure); }
  };
}
