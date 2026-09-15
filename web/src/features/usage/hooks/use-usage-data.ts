"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import type { EffectiveLimit, LimitPolicy, OutboxStatus, PriceVersion, UnresolvedAttempt, UsageFilters } from "@/features/usage/types/usage.types";
import { emptyUsage, failureText, readUsageFilters } from "@/features/usage/utils/usage.utils";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useUsageData(canManage: boolean) {
  const [usage, setUsage] = useState(emptyUsage);
  const [unresolved, setUnresolved] = useState<UnresolvedAttempt[]>([]);
  const [policies, setPolicies] = useState<LimitPolicy[]>([]);
  const [prices, setPrices] = useState<PriceVersion[]>([]);
  const [policyCursor, setPolicyCursor] = useState("");
  const [priceCursor, setPriceCursor] = useState("");
  const [unresolvedCursor, setUnresolvedCursor] = useState("");
  const [outbox, setOutbox] = useState<OutboxStatus | null>(null);
  const [limits, setLimits] = useState<EffectiveLimit[]>([]);
  const [filters, setFilters] = useState<UsageFilters>({});
  const [filtersReady, setFiltersReady] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const loadGeneration = useRef(0);

  async function load() {
    const generation = ++loadGeneration.current;
    try {
      const [summary, unknown, policyList] = await Promise.all([
        pocketAIGatewayAdmin.usage.summary(filters),
        pocketAIGatewayAdmin.usage.unresolved(filters),
        pocketAIGatewayAdmin.usage.policies(),
      ]);
      if (generation !== loadGeneration.current) return;
      setUsage(summary.usage);
      setUnresolved(unknown.data);
      setUnresolvedCursor(unknown.next_cursor);
      setPolicies(policyList.data);
      setPolicyCursor(policyList.next_cursor);
      if (canManage) {
        const [priceList, outboxState] = await Promise.all([pocketAIGatewayAdmin.usage.prices(), pocketAIGatewayAdmin.usage.outbox()]);
        if (generation !== loadGeneration.current) return;
        setPrices(priceList.data);
        setPriceCursor(priceList.next_cursor);
        setOutbox(outboxState.outbox);
      }
      setError("");
    } catch (failure) {
      if (generation === loadGeneration.current) setError(failureText(failure, "Usage data is unavailable"));
    }
  }

  useEffect(() => {
    const sync = () => {
      const next = readUsageFilters();
      const query = new URLSearchParams(next as Record<string, string>);
      const canonical = query.size ? `?${query}` : "";
      if (canonical !== window.location.search) window.history.replaceState(null, "", `${window.location.pathname}${canonical}`);
      setFilters(next);
      setFiltersReady(true);
    };
    sync();
    window.addEventListener("popstate", sync);
    return () => window.removeEventListener("popstate", sync);
  }, []);

  useEffect(() => {
    if (filtersReady) void load();
  }, [canManage, filters, filtersReady]);

  async function loadMore(kind: "policies" | "prices" | "unresolved") {
    const generation = loadGeneration.current;
    setBusy(true);
    try {
      if (kind === "policies") {
        const page = await pocketAIGatewayAdmin.usage.policies(policyCursor);
        if (generation !== loadGeneration.current) return;
        setPolicies((items) => [...items, ...page.data]);
        setPolicyCursor(page.next_cursor);
      } else if (kind === "prices") {
        const page = await pocketAIGatewayAdmin.usage.prices(priceCursor);
        if (generation !== loadGeneration.current) return;
        setPrices((items) => [...items, ...page.data]);
        setPriceCursor(page.next_cursor);
      } else {
        const page = await pocketAIGatewayAdmin.usage.unresolved(filters, unresolvedCursor);
        if (generation !== loadGeneration.current) return;
        setUnresolved((items) => [...items, ...page.data]);
        setUnresolvedCursor(page.next_cursor);
      }
    } catch (failure) {
      if (generation === loadGeneration.current) setError(failureText(failure, "The next page could not be loaded"));
    } finally {
      if (generation === loadGeneration.current) setBusy(false);
    }
  }

  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const next: UsageFilters = {};
    for (const key of ["from", "to", "user_id", "key_id", "model_id", "connection_id"] as const) {
      const value = String(form.get(key) ?? "");
      if (value) next[key] = key === "from" || key === "to" ? new Date(value).toISOString() : value;
    }
    setFilters(next);
    const query = new URLSearchParams(next as Record<string, string>);
    window.history.replaceState(null, "", query.size ? `?${query}` : window.location.pathname);
  }

  async function inspectLimits(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.usage.effectiveLimits(String(form.get("key_id")), String(form.get("connection_id")));
      setLimits(result.data);
    } catch (failure) {
      setError(failureText(failure, "Effective limits are unavailable"));
    } finally {
      setBusy(false);
    }
  }

  return { usage, unresolved, policies, prices, policyCursor, priceCursor, unresolvedCursor, outbox, limits, filters, error, busy, load, loadMore, applyFilters, inspectLimits };
}
