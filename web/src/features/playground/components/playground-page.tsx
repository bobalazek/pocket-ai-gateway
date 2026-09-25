"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { usePlayground } from "@/features/playground/hooks/use-playground";

export default function PlaygroundPage() {
  const { result, error, busy, stream, setStream, submit, cancel } = usePlayground();
  return <AppShell active="Playground"><main id="main-content" className="content management-page"><header className="page-header"><div><p className="context">Native protocol check</p><h1>Playground</h1><p className="lede">Send a small request through a scoped gateway key. The key stays in this form and is not stored.</p></div></header><Card className="panel"><form onSubmit={submit}><div className="inline-fields"><div className="field"><Label htmlFor="protocol">Protocol</Label><select className="select" id="protocol" name="protocol"><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="gemini">Gemini</option></select></div><Field id="model" label="Public model ID" /></div><Field id="key" label="Gateway API key" type="password" /><div className="field"><Label htmlFor="prompt">Prompt</Label><textarea className="select" id="prompt" name="prompt" rows={5} required /></div><Switch checked={stream} onChange={(event) => setStream(event.target.checked)} label="Stream response" />{error && <p className="form-error" role="alert">{error}</p>}<div className="row-actions"><Button disabled={busy}>{busy ? "Sending…" : "Send request"}</Button>{busy && <Button type="button" variant="outline" onClick={cancel}>Cancel</Button>}</div></form></Card>{result && <Card className="panel section-block" role="status" aria-live="polite"><h2>Response</h2><pre className="code-block" aria-label="Gateway response" tabIndex={0}>{result}</pre></Card>}</main></AppShell>;
}

function Field({ id, label, type = "text" }: { id: string; label: string; type?: string }) {
  return <div className="field"><Label htmlFor={id}>{label}</Label><Input id={id} name={id} type={type} required /></div>;
}
