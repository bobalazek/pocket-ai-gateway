import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { OperationalStatus } from "@/features/status/components/operational-status";
import { RuntimeHealth } from "@/features/status/components/runtime-health";

export default function StatusPage() {
  return (
    <AppShell active="Status">
      <main id="main-content" className="content management-page status-dashboard">
        <header className="page-header"><div>
          <h1>Status</h1>
          <p className="lede">See whether this gateway is online, accepting requests, and keeping up with accounting.</p>
        </div></header>
        <OperationalStatus />
        <section className="panel public-probes" aria-label="Public runtime checks">
          <h2>Public probes</h2>
          <div className="status-list"><RuntimeHealth compact /></div>
          <p className="fine-print">Health confirms the HTTP process only. Readiness checks local databases and accounting capacity.</p>
          <div className="row-actions">
            <a className={buttonVariants({ variant: "outline" })} href="/healthz">Open health response</a>
            <a className={buttonVariants({ variant: "outline" })} href="/readyz">Open readiness response</a>
          </div>
        </section>
      </main>
    </AppShell>
  );
}
