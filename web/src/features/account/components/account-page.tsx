"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAccount } from "@/features/account/hooks/use-account";

export default function AccountPage() {
  const { user, sessions, message, error, updateProfile, changePassword, logout, revoke } = useAccount();

  return (
    <AppShell active="Account">
      <main id="main-content" className="content management-page">
        <header className="page-header"><div><p className="context">Account</p><h1>Your profile and sessions.</h1><p className="lede">Email changes require your current password and sign out other sessions.</p></div><Button variant="outline" onClick={logout}>Sign out</Button></header>
        {error && <p className="form-error" role="alert">{error}</p>}{message && <p className="form-success" role="status">{message}</p>}
        <div className="settings-grid">
          <Card className="panel"><h2>Profile</h2><form onSubmit={updateProfile}>
            <div className="field"><Label htmlFor="display_name">Name</Label><Input id="display_name" name="display_name" defaultValue={user?.display_name} required /></div>
            <div className="field"><Label htmlFor="email">Email</Label><Input id="email" name="email" type="email" defaultValue={user?.email} required /></div>
            <div className="field"><Label htmlFor="profile-password">Current password</Label><Input id="profile-password" name="current_password" type="password" autoComplete="current-password" /><small>Required only when changing email.</small></div>
            <Button type="submit">Save profile</Button>
          </form></Card>
          <Card className="panel"><h2>Password</h2><form onSubmit={changePassword}>
            <div className="field"><Label htmlFor="current-password">Current password</Label><Input id="current-password" name="current_password" type="password" autoComplete="current-password" required /></div>
            <div className="field"><Label htmlFor="new-password">New password</Label><Input id="new-password" name="new_password" type="password" autoComplete="new-password" minLength={12} maxLength={1024} required /></div>
            <Button type="submit">Change password</Button>
          </form></Card>
        </div>
        <section className="section-block"><div className="section-heading"><div><p className="context">Security</p><h2>Active sessions</h2></div></div>
          <div className="resource-list">{sessions.map((session) => <Card className="resource-row" key={session.id}><div><strong>{session.current ? "This browser" : session.user_agent || "Unknown browser"}</strong><small>Created {new Date(session.created_at).toLocaleString()} · expires {new Date(session.expires_at).toLocaleString()}</small></div><Button variant="outline" onClick={() => revoke(session.id, session.current)}>Sign out</Button></Card>)}</div>
        </section>
      </main>
    </AppShell>
  );
}
