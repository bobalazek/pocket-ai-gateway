import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { RuntimeHealth } from "@/features/status/components/runtime-health";


export default function StatusPage() {
  return (
    <AppShell active="Status">
      <main id="main-content" className="status-page">
        <Link className="back-link" href="/">← Overview</Link>
        <header>
          <p className="context">System status</p>
          <h1>Runtime status</h1>
          <p className="lede">Safe, non-secret details for this Pocket AI Gateway process.</p>
        </header>
        <section className="status-list" aria-label="Runtime checks">
          <RuntimeHealth compact />
        </section>
		<p className="fine-print">Health confirms the HTTP process only. Sign in to inspect configuration, providers, storage, and accounting.</p>
		<div className="row-actions"><a className={buttonVariants()} href="/healthz">Open health response</a><a className={buttonVariants({ variant: "outline" })} href="/readyz">Open readiness response</a></div>
      </main>
    </AppShell>
  );
}
