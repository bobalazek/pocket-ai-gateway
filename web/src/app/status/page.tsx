import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { buttonVariants } from "@/components/ui/button";

export const metadata = { title: "Runtime status" };

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
          <div><span>HTTP server</span><strong>Ready</strong></div>
          <div><span>Dashboard assets</span><strong>Embedded</strong></div>
          <div><span>Local SQLite stores</span><strong>Ready</strong></div>
        </section>
		<p className="fine-print">Identity, user grants, and API keys are ready. Provider connections, limits, and inference arrive in the next phases.</p>
        <a className={buttonVariants()} href="/healthz">Open health response</a>
      </main>
    </AppShell>
  );
}
