"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI } from "@/lib/api-client";

export default function PlaygroundPage() {
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
    const protocol = String(form.get("protocol")) as "openai" | "anthropic" | "gemini";
    const key = String(form.get("key"));
    const model = String(form.get("model"));
    const prompt = String(form.get("prompt"));
    const controller = new AbortController();
    abort.current = controller;
    setBusy(true);
    setError("");
    setResult("");
    try {
      if (stream) await gatewayAPI.streamGenerate(protocol, key, model, prompt, (chunk) => { if (mounted.current) setResult((value) => value + chunk); }, controller.signal);
      else {
        const response = await gatewayAPI.generate(protocol, key, model, prompt, controller.signal);
        if (mounted.current) setResult(JSON.stringify(response, null, 2));
      }
    } catch (failure) {
      if (mounted.current) setError(controller.signal.aborted ? "Request cancelled" : failure instanceof GatewayAPIError ? failure.message : "Request failed");
    } finally {
      abort.current = null;
      if (mounted.current) setBusy(false);
    }
  }
  return <AppShell active="Playground"><main id="main-content" className="content management-page"><header className="page-header"><div><p className="context">Native protocol check</p><h1>Playground</h1><p className="lede">Send a small request through a scoped gateway key. The key stays in this form and is not stored.</p></div></header><Card className="panel"><form onSubmit={submit}><div className="inline-fields"><div className="field"><Label htmlFor="protocol">Protocol</Label><select className="select" id="protocol" name="protocol"><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="gemini">Gemini</option></select></div><Field id="model" label="Public model ID" /></div><Field id="key" label="Gateway API key" type="password" /><div className="field"><Label htmlFor="prompt">Prompt</Label><textarea className="select" id="prompt" name="prompt" rows={5} required /></div><label className="checkbox-row"><input type="checkbox" checked={stream} onChange={(event) => setStream(event.target.checked)} /> Stream response</label>{error && <p className="form-error" role="alert">{error}</p>}<div className="row-actions"><Button disabled={busy}>{busy ? "Sending…" : "Send request"}</Button>{busy && <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>Cancel</Button>}</div></form></Card>{result && <Card className="panel section-block" role="status" aria-live="polite"><h2>Response</h2><pre className="code-block" aria-label="Gateway response" tabIndex={0}>{result}</pre></Card>}</main></AppShell>;
}

function Field({ id, label, type = "text" }: { id: string; label: string; type?: string }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} type={type} required /></div>;
}
