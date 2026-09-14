"use client";

import Link from "next/link";
import { useEffect, useState, type FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI } from "@/lib/api-client";

export default function LoginPage() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
	const [expired, setExpired] = useState(false);
	useEffect(() => setExpired(new URLSearchParams(window.location.search).get("reason") === "session-expired"), []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await gatewayAPI.login({ email: String(form.get("email") ?? ""), password: String(form.get("password") ?? "") });
      window.location.replace("/_/");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Login could not be completed");
      setSubmitting(false);
    }
  }

  return (
    <main id="main-content" className="auth-shell">
      <div className="brand setup-brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
      <Card className="auth-card">
        <p className="context">Welcome back</p>
        <h1>Sign in.</h1>
        <p className="lede">Use an account created by your gateway administrator.</p>
		{expired && <p className="form-error" role="status">Your session expired. Sign in again to continue.</p>}
        <form onSubmit={submit}>
          <div className="field"><Label htmlFor="email">Email</Label><Input id="email" name="email" type="email" autoComplete="email" required /></div>
          <div className="field"><Label htmlFor="password">Password</Label><Input id="password" name="password" type="password" autoComplete="current-password" required /></div>
          {error && <p className="form-error" role="alert">{error}</p>}
          <Button type="submit" disabled={submitting}>{submitting ? "Signing in…" : "Sign in"}</Button>
        </form>
        <p className="fine-print">Have an activation or recovery code? <Link className="text-link" href="/activate/">Set your password</Link>.</p>
      </Card>
    </main>
  );
}
