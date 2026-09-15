"use client";


import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useSetup } from "@/features/setup/hooks/use-setup";

export default function SetupPage() {
  const { error, submitting, submit } = useSetup();

  return (
    <main id="main-content" className="setup-shell">
      <div className="setup-intro">
        <div className="brand setup-brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
        <p className="context">First-time setup</p>
        <h1>Create the owner account.</h1>
        <p className="lede">Create the first account for this gateway. The owner can add administrators and manage recovery later.</p>
      </div>
      <Card className="setup-card">
        <form onSubmit={submit}>
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
