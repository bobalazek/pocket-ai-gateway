"use client";

import Link from "next/link";
import { useState, type FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI } from "@/lib/api-client";

export default function ActivatePage() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await gatewayAPI.activate({ code: String(form.get("code") ?? ""), password: String(form.get("password") ?? "") });
      window.location.replace("/_/");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Activation could not be completed");
      setSubmitting(false);
    }
  }

  return (
    <main id="main-content" className="auth-shell">
      <div className="brand setup-brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
      <Card className="auth-card">
        <p className="context">Account access</p>
        <h1>Set your password.</h1>
        <p className="lede">An activation or recovery code works once and expires after 24 hours.</p>
        <form onSubmit={submit}>
          <div className="field"><Label htmlFor="code">Code</Label><Input id="code" name="code" autoComplete="one-time-code" required /></div>
          <div className="field"><Label htmlFor="password">New password</Label><Input id="password" name="password" type="password" autoComplete="new-password" minLength={12} maxLength={1024} required /><small>Use at least 12 characters.</small></div>
          {error && <p className="form-error" role="alert">{error}</p>}
          <Button type="submit" disabled={submitting}>{submitting ? "Saving…" : "Set password"}</Button>
        </form>
        <p className="fine-print"><Link className="text-link" href="/login/">Back to sign in</Link></p>
      </Card>
    </main>
  );
}
