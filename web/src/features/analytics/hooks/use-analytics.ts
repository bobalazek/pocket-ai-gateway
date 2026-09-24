"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { UsageBreakdown, UsageBreakdownDimension, UsageBreakdownSort, UsageFilters } from "@/features/usage/types/usage.types";
import { emptyUsage, failureText, readUsageFilters } from "@/features/usage/utils/usage.utils";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

type RankingKey = `${UsageBreakdownDimension}:${UsageBreakdownSort}`;
const rankingQueries: [UsageBreakdownDimension, UsageBreakdownSort][] = [
  ["key", "requests"], ["key", "known_cost"], ["key", "tokens"], ["key", "p95_latency"],
  ["user", "requests"], ["user", "known_cost"],
  ["model", "requests"], ["model", "known_cost"], ["model", "p95_latency"],
  ["connection", "requests"], ["connection", "known_cost"], ["connection", "p95_latency"],
  ["dialect", "requests"], ["operation", "requests"], ["state", "requests"],
];
const rankingKey = (dimension: UsageBreakdownDimension, sort: UsageBreakdownSort): RankingKey => `${dimension}:${sort}`;

function periodFilters(days: number, now = new Date()): UsageFilters {
  const from = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - days + 1));
  return { from: from.toISOString(), to: now.toISOString() };
}

function writeFilters(filters: UsageFilters, replace = false, preset: number | null = null) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
  if (preset) query.set("period", String(preset));
  window.history[replace ? "replaceState" : "pushState"](null, "", query.size ? `?${query}` : window.location.pathname);
}

export function useAnalytics() {
  const user = useGatewayUser();
  const [filters, setFilters] = useState<UsageFilters>({});
  const [preset, setPreset] = useState<number | null>(null);
  const [ready, setReady] = useState(false);
  const [revision, setRevision] = useState(0);
  const [usage, setUsage] = useState(emptyUsage);
  const [rankings, setRankings] = useState<Partial<Record<RankingKey, UsageBreakdown>>>({});
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState<RankingKey | null>(null);
  const [error, setError] = useState("");
  const generation = useRef(0);

  useEffect(() => {
    const sync = () => {
      const next = readUsageFilters();
      const value = Number(new URLSearchParams(window.location.search).get("period"));
      const selected = [7, 30, 90].includes(value) ? value : !next.from && !next.to ? 7 : null;
      if (selected) {
        Object.assign(next, periodFilters(selected));
        writeFilters(next, true, selected);
      }
      setFilters(next);
      setPreset(selected);
      setReady(true);
    };
    sync();
    window.addEventListener("popstate", sync);
    return () => window.removeEventListener("popstate", sync);
  }, []);

  useEffect(() => {
    if (!ready) return;
    const controller = new AbortController();
    const current = ++generation.current;
    setLoading(true);
    setLoadingMore(null);
    setUsage(emptyUsage);
    setRankings({});
    setError("");
    (async () => {
      const summary = await pocketAIGatewayAdmin.usage.summary(filters, controller.signal);
      const alignedFilters = { ...filters, from: summary.usage.from, to: summary.usage.to };
      const groups = await Promise.all(rankingQueries.map(([dimension, sort]) => pocketAIGatewayAdmin.usage.breakdown(dimension, alignedFilters, sort, 0, controller.signal)));
      return { summary, groups };
    })().then(({ summary, groups }) => {
      if (current !== generation.current) return;
      setUsage(summary.usage);
      setRankings(Object.fromEntries(groups.map((group) => [rankingKey(group.dimension, group.sort), group])) as Record<RankingKey, UsageBreakdown>);
      setError("");
    }).catch((failure: unknown) => {
      if (!controller.signal.aborted && current === generation.current) setError(failureText(failure, "Analytics are unavailable"));
    }).finally(() => {
      if (current === generation.current) setLoading(false);
    });
    return () => controller.abort();
  }, [filters, ready, revision]);

  function updateFilters(next: UsageFilters, nextPreset = preset) {
    writeFilters(next, false, nextPreset);
    setFilters(next);
    setPreset(nextPreset);
  }

  function drillDown(kind: "user_id" | "key_id" | "model_id" | "connection_id", id: string) {
    updateFilters({ ...filters, [kind]: id });
  }

  function clearDimension(kind: "user_id" | "key_id" | "model_id" | "connection_id") {
    const next = { ...filters };
    delete next[kind];
    updateFilters(next);
  }

  function setPeriod(days: number) {
    updateFilters({ ...filters, ...periodFilters(days) }, days);
  }

  function applyCustomRange(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const from = String(form.get("from") ?? "");
    const to = String(form.get("to") ?? "");
    const next: UsageFilters = { ...filters };
    if (from) next.from = new Date(from).toISOString(); else delete next.from;
    if (to) next.to = new Date(to).toISOString(); else delete next.to;
    if (user?.role === "owner" || user?.role === "admin") {
      const userID = String(form.get("user_id") ?? "").trim();
      if (userID) next.user_id = userID; else delete next.user_id;
    }
    updateFilters(next, null);
  }

  function refresh() {
    if (preset) {
      const next = { ...filters, ...periodFilters(preset) };
      writeFilters(next, true, preset);
      setFilters(next);
    } else {
      setRevision((value) => value + 1);
    }
  }

  async function loadMore(dimension: UsageBreakdownDimension, sort: UsageBreakdownSort = "requests") {
    const key = rankingKey(dimension, sort);
    const previous = rankings[key];
    if (!previous?.has_more || previous.next_offset === null) return;
    const current = generation.current;
    setLoadingMore(key);
    try {
      const next = await pocketAIGatewayAdmin.usage.breakdown(dimension, { ...filters, from: usage.from, to: usage.to }, sort, previous.next_offset);
      if (current === generation.current) setRankings((items) => ({
        ...items,
        [key]: { ...next, data: [...(items[key]?.data ?? []), ...next.data] },
      }));
    } catch (failure) {
      if (current === generation.current) setError(failureText(failure, "More analytics could not be loaded"));
    } finally {
      if (current === generation.current) setLoadingMore(null);
    }
  }

  return {
    user, usage, rankings, filters, preset, loading, loadingMore, error, canManage: user?.role === "owner" || user?.role === "admin",
    drillDown, clearDimension, setPeriod, applyCustomRange, loadMore, refresh,
  };
}
