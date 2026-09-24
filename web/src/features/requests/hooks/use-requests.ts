"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { readRequestFilters, requestListFilters, requestSearch } from "@/features/requests/utils/requests.utils";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useRequests() {
  const active = useRef<AbortController | null>(null);
  const [items, setItems] = useState<GatewayRequest[]>([]);
  const [filters, setFilters] = useState<RequestFilters>({});
  const [next, setNext] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  async function load(value: RequestFilters) {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setLoading(true);
    try {
      const page = await pocketAIGatewayAdmin.requests.list(value, controller.signal);
      if (controller.signal.aborted) return;
      setItems(page.data);
      setNext(page.next_cursor);
      setError("");
    } catch (failure) {
      if (controller.signal.aborted) return;
      setItems([]);
      setNext("");
      setError(failure instanceof GatewayAPIError ? failure.message : "Requests are unavailable");
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }

  useEffect(() => {
    const sync = () => {
      const value = readRequestFilters(window.location.search);
      setFilters(value);
      void load(value);
    };
    sync();
    window.addEventListener("popstate", sync);
    return () => {
      active.current?.abort();
      window.removeEventListener("popstate", sync);
    };
  }, []);

  function navigate(value: RequestFilters) {
    const query = requestSearch(value);
    window.history.pushState({}, "", query ? `?${query}` : window.location.pathname);
    setFilters(value);
    void load(value);
  }

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const value: RequestFilters = {};
    for (const key of ["user_id", "key_id", "model_id", "connection_id", "dialect", "operation", "state", "from", "to"] as const) {
      const field = String(form.get(key) ?? "");
      if (field) value[key] = key === "from" || key === "to" ? new Date(field).toISOString() : field;
    }
    navigate(value);
  }

  function openDetail(id: string) {
    const value = { ...requestListFilters(filters), request_id: id };
    const query = requestSearch(value);
    window.history.pushState({ requestDetail: true }, "", `?${query}`);
    setFilters(value);
    void load(value);
  }

  function closeDetail() {
    if (window.history.state?.requestDetail === true) {
      window.history.back();
      return;
    }
    const value = requestListFilters(filters);
    const query = requestSearch(value);
    window.history.replaceState({}, "", query ? `?${query}` : window.location.pathname);
    setFilters(value);
    void load(value);
  }

  return { items, filters, next, error, loading, apply, navigate, openDetail, closeDetail };
}
