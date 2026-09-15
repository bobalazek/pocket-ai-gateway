"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import { inferenceScopes } from "@/features/auth/constants/inference-scopes.constants";
import type { GatewayKey, OneTimeSecret } from "@/features/keys/types/keys.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

const list = (value: FormDataEntryValue | null) => String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);

export function useKeys() {
  const current = useGatewayUser();
  const [keys, setKeys] = useState<GatewayKey[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [secret, setSecret] = useState<OneTimeSecret | null>(null);
  const [error, setError] = useState("");
  const [issuing, setIssuing] = useState(false);
  const [loadingPage, setLoadingPage] = useState(false);
  const issuingRef = useRef(false); const loadingRef = useRef(false); const pageRequestRef = useRef(0);
  const showError = (failure: unknown) => setError(failure instanceof GatewayAPIError ? failure.message : "Request failed");

  async function loadPage(cursor = "", push = false, supersede = false) {
    if (loadingRef.current && !supersede) return;
    const request = ++pageRequestRef.current; loadingRef.current = true; setLoadingPage(true);
    try { const page = await pocketAIGatewayAdmin.keys.list(cursor); if (request !== pageRequestRef.current) return; setKeys(page.data); setNextCursor(page.next_cursor); if (push) window.history.pushState({}, "", cursor ? `?cursor=${encodeURIComponent(cursor)}` : window.location.pathname); }
    catch (failure) { if (request === pageRequestRef.current) showError(failure); }
    finally { if (request === pageRequestRef.current) { loadingRef.current = false; setLoadingPage(false); } }
  }
  async function refresh() { await loadPage(new URLSearchParams(window.location.search).get("cursor") ?? ""); }
  useEffect(() => { const reload = () => void loadPage(new URLSearchParams(window.location.search).get("cursor") ?? "", false, true); void reload(); window.addEventListener("popstate", reload); return () => window.removeEventListener("popstate", reload); }, []);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setSecret(null); if (issuingRef.current) return; issuingRef.current = true; setIssuing(true);
    const formElement = event.currentTarget; const form = new FormData(formElement);
    try { const expiry = String(form.get("expires_at") ?? ""); const result = await pocketAIGatewayAdmin.keys.create({ label: String(form.get("label")), scopes: form.getAll("scopes").map(String), model_patterns: list(form.get("models")), connection_ids: list(form.get("connections")), expires_at: expiry ? new Date(expiry).toISOString() : "" }); setSecret({ value: result.secret, label: result.key.label, keyID: result.key.id }); formElement.reset(); await refresh(); }
    catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); }
  }
  async function rotate(key: GatewayKey) { if (issuingRef.current || !window.confirm(`Rotate ${key.label}? Its current secret will stop working immediately.`)) return; issuingRef.current = true; setSecret(null); setIssuing(true); try { const result = await pocketAIGatewayAdmin.keys.rotate(key.id, key.revision); setSecret({ value: result.secret, label: result.key.label, keyID: result.key.id }); await refresh(); } catch (failure) { showError(failure); } finally { issuingRef.current = false; setIssuing(false); } }
  async function revoke(key: GatewayKey) { if (!window.confirm(`Revoke ${key.label}? This cannot be undone.`)) return; setSecret(null); try { await pocketAIGatewayAdmin.keys.revoke(key.id, key.revision); await refresh(); } catch (failure) { showError(failure); } }
  async function toggle(key: GatewayKey) { try { await pocketAIGatewayAdmin.keys.update(key.id, key.revision, { label: key.label, state: key.state === "active" ? "disabled" : "active", scopes: key.scopes, model_patterns: key.model_patterns, connection_ids: key.connection_ids, expires_at: key.expires_at ?? "" }); await refresh(); } catch (failure) { showError(failure); } }

  const permittedScopes = current?.grants.unrestricted ? [...inferenceScopes] : current?.grants.scopes ?? [];
  return { current, keys, nextCursor, secret, error, issuing, loadingPage, permittedScopes, create, rotate, revoke, toggle, loadPage };
}
