"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import type { GatewayRequest, RequestFilters } from "@/features/requests/types/requests.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

function readFilters(): RequestFilters {
  const query = new URLSearchParams(window.location.search);
  const result: RequestFilters = {};
  for (const key of ["user_id", "key_id", "model_id", "dialect", "cursor"] as const) {
    const value = query.get(key);
    if (value) result[key] = value;
  }
  return result;
}

export function useRequests() {
  const active = useRef<AbortController | null>(null);
  const [items, setItems] = useState<GatewayRequest[]>([]);
  const [filters, setFilters] = useState<RequestFilters>({});
  const [next, setNext] = useState("");
  const [error, setError] = useState("");

  async function load(value: RequestFilters) {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    try {
      const page = await pocketAIGatewayAdmin.requests.list(value, controller.signal);
      if (controller.signal.aborted) return;
      setItems(page.data);
      setNext(page.next_cursor);
      setError("");
    } catch (failure) {
      if (!controller.signal.aborted) setError(failure instanceof GatewayAPIError ? failure.message : "Requests are unavailable");
    }
  }

  useEffect(() => {
    const sync = () => {
      const value = readFilters();
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
    const query = new URLSearchParams(value as Record<string, string>);
    window.history.pushState({}, "", query.size ? `?${query}` : window.location.pathname);
    setFilters(value);
    void load(value);
  }

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const value: RequestFilters = {};
    for (const key of ["user_id", "key_id", "model_id", "dialect"] as const) {
      const field = String(form.get(key) ?? "");
      if (field) value[key] = field;
    }
    navigate(value);
  }

  return { items, filters, next, error, apply, navigate };
}
