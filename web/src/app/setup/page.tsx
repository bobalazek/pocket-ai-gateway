"use client";

import { useState, type FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI } from "@/lib/api-client";

export default function SetupPage() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await gatewayAPI.claimOwner({
        setup_code: String(form.get("setup_code") ?? ""),
        display_name: String(form.get("display_name") ?? ""),
        email: String(form.get("email") ?? ""),
        password: String(form.get("password") ?? ""),
      });
      window.location.replace("/_/providers/");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Setup could not be completed");
      setSubmitting(false);
    }
  }

  return (
    <main id="main-content" className="setup-shell">
      <div className="setup-intro">
        <div className="brand setup-brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
        <p className="context">First-time setup</p>
        <h1>Create the owner account.</h1>
        <p className="lede">Use the one-time code stored in the protected setup file named by the gateway. This account controls recovery and can add administrators later.</p>
      </div>
      <Card className="setup-card">
        <form onSubmit={submit}>
          <div className="field"><Label htmlFor="setup-code">Setup code</Label><Input id="setup-code" name="setup_code" autoComplete="one-time-code" required /></div>
          <div className="field"><Label htmlFor="display-name">Name</Label><Input id="display-name" name="display_name" autoComplete="name" maxLength={100} required /></div>
          <div className="field"><Label htmlFor="email">Email</Label><Input id="email" name="email" type="email" autoComplete="email" maxLength={254} required /></div>
          <div className="field"><Label htmlFor="password">Password</Label><Input id="password" name="password" type="password" autoComplete="new-password" minLength={12} maxLength={1024} required /><small>Use at least 12 characters.</small></div>
          {error && <p className="form-error" role="alert">{error}</p>}
          <Button type="submit" disabled={submitting}>{submitting ? "Creating owner…" : "Create owner"}</Button>
        </form>
      </Card>
    </main>
  );
}
