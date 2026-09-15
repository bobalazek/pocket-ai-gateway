"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useEffect, useRef, useState, type FormEvent } from "react";

import type { AuditEvent, AuditFilters } from "@/features/audit/types/audit.types";
import { canonicalTime } from "@/features/audit/utils/audit.utils";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

const keys = ["actor_user_id", "action", "resource_type", "from", "to", "cursor"] as const;

function readFilters() {
  const query = new URLSearchParams(window.location.search); const result: AuditFilters = {}; let changed = false;
  for (const key of keys) {
    let value = query.get(key) ?? "";
    if ((key === "from" || key === "to") && value) { const canonical = canonicalTime(value); if (!canonical) { query.delete(key); changed = true; value = ""; } else if (canonical !== value) { query.set(key, canonical); changed = true; value = canonical; } }
    if (value) result[key] = value;
  }
  if (changed) window.history.replaceState(window.history.state, "", query.size ? `?${query}` : window.location.pathname);
  return result;
}

export function useAudit() {
  const active = useRef<AbortController | null>(null);
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [filters, setFilters] = useState<AuditFilters>({});
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [depth, setDepth] = useState(0);

  async function load(query: AuditFilters) {
    active.current?.abort(); const controller = new AbortController(); active.current = controller;
    setBusy(true); setItems([]); setNext("");
    try { const result = await pocketAIGatewayAdmin.audit.list(query, controller.signal); if (controller.signal.aborted) return; setItems(result.data ?? []); setNext(result.next_cursor); setError(""); }
    catch (failure) { if (!controller.signal.aborted) setError(failure instanceof GatewayAPIError ? failure.message : "Audit events are unavailable"); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }

  useEffect(() => {
    const sync = () => { const query = readFilters(); const pageDepth = Number(window.history.state?.auditDepth ?? 0); setDepth(pageDepth); setFilters(query); void load(query); };
    window.history.replaceState({ ...window.history.state, auditDepth: Number(window.history.state?.auditDepth ?? 0) }, "");
    sync(); window.addEventListener("popstate", sync);
    return () => { active.current?.abort(); window.removeEventListener("popstate", sync); };
  }, []);

  function navigate(query: AuditFilters) {
    const values = new URLSearchParams(); for (const [key, value] of Object.entries(query)) if (value) values.set(key, value);
    const nextDepth = depth + 1; window.history.pushState({ ...window.history.state, auditDepth: nextDepth }, "", values.size ? `?${values}` : window.location.pathname);
    setDepth(nextDepth); setFilters(query); void load(query);
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    const text = (name: string) => String(form.get(name) ?? "").trim();
    const isoTime = (value: string) => value ? new Date(value).toISOString() : "";
    navigate({ actor_user_id: text("actor_user_id"), action: text("action"), resource_type: text("resource_type"), from: isoTime(text("from")), to: isoTime(text("to")) });
  }

  return { items, filters, next, busy, error, depth, navigate, submit };
}
