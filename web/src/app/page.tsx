import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";

export default function OverviewPage() {
  return (
    <AppShell active="Overview">
      <main id="main-content" className="content">
        <header className="page-header">
          <div>
            <p className="context">Dashboard</p>
            <h1>Your gateway is ready to configure.</h1>
            <p className="lede">Manage access, enforce usage limits, and review local accounting before connecting providers.</p>
          </div>
          <a className={buttonVariants()} href="/healthz">Check health</a>
        </header>

        <Card className="readiness" role="region" aria-labelledby="readiness-title">
          <CardHeader className="section-heading">
            <h2 id="readiness-title">Runtime readiness</h2>
            <span className="status"><span aria-hidden="true" /> Ready</span>
          </CardHeader>
          <CardContent>
            <dl className="facts">
              <div><dt>Identity</dt><dd>Owner, admin, and member roles</dd></div>
              <div><dt>API keys</dt><dd>Scoped, rotating secrets</dd></div>
              <div><dt>Accounting</dt><dd>Atomic limits and local usage</dd></div>
            </dl>
          </CardContent>
        </Card>

        <section className="next-step" aria-labelledby="next-step-title">
          <div>
            <h2 id="next-step-title">Continue setup</h2>
            <p>Add users and application keys, then configure limits and model prices. Provider connections and live traffic arrive in the next phase.</p>
          </div>
          <div className="row-actions"><Link className="text-link" href="/usage/">Configure usage <span aria-hidden="true">→</span></Link><Link className="text-link" href="/keys/">Manage API keys</Link></div>
        </section>
      </main>
    </AppShell>
  );
}
