"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useEffect, useRef, useState, type FormEvent } from "react";

import type { PlaygroundProtocol } from "@/features/playground/types/playground.types";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function usePlayground() {
  const abort = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [stream, setStream] = useState(false);

  useEffect(() => () => { mounted.current = false; abort.current?.abort(); }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const protocol = String(form.get("protocol")) as PlaygroundProtocol;
    const key = String(form.get("key")); const model = String(form.get("model")); const prompt = String(form.get("prompt"));
    const controller = new AbortController(); abort.current = controller;
    setBusy(true); setError(""); setResult("");
    try {
      if (stream) await pocketAIGatewayAdmin.playground.stream(protocol, key, model, prompt, (chunk) => { if (mounted.current) setResult((value) => value + chunk); }, controller.signal);
      else { const response = await pocketAIGatewayAdmin.playground.generate(protocol, key, model, prompt, controller.signal); if (mounted.current) setResult(JSON.stringify(response, null, 2)); }
    } catch (failure) {
      if (mounted.current) setError(controller.signal.aborted ? "Request cancelled" : failure instanceof GatewayAPIError ? failure.message : "Request failed");
    } finally {
      abort.current = null;
      if (mounted.current) setBusy(false);
    }
  }

  return { result, error, busy, stream, setStream, submit, cancel: () => abort.current?.abort() };
}
