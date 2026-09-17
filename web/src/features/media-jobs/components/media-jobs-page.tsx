"use client";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useMediaJobs } from "@/features/media-jobs/hooks/use-media-jobs";

export default function MediaJobsPage() {
  const model = useMediaJobs();
  if (model.user?.role === "member") {
    return <AppShell active="Media jobs"><main id="main-content" className="content"><h1>Administrator access required.</h1></main></AppShell>;
  }
  return (
    <AppShell active="Media jobs">
      <main id="main-content" className="content management-page">
        <header className="page-header">
          <div><p className="context">Asynchronous generation</p><h1>Media jobs</h1><p className="lede">Inspect Replicate and Together image, audio, video, and custom prediction jobs.</p></div>
          <Button type="button" variant="outline" disabled={model.busy} onClick={() => void model.load()}>Refresh</Button>
        </header>
        {model.error && <p className="form-error" role="alert">{model.error}</p>}
        <section className="resource-list" aria-live="polite">
          {model.items.map((item) => (
            <Card className="panel" key={item.id}>
              <div className="resource-row-main">
                <div>
                  <strong>{item.media_type} · {item.state}</strong>
                  <small>{item.provider} · {item.model} · updated {new Date(item.updated_at).toLocaleString()}</small>
                  <code>{item.id}{item.provider_job_id ? ` · ${item.provider_job_id}` : ""}</code>
                  {item.error?.message && <small className="form-error">{item.error.message}</small>}
                </div>
                {item.cancelable && <Button type="button" variant="outline" disabled={model.busy} onClick={() => void model.cancel(item)}>Cancel</Button>}
              </div>
            </Card>
          ))}
          {!model.error && model.items.length === 0 && <Card className="panel"><p>No media jobs have been submitted.</p></Card>}
        </section>
      </main>
    </AppShell>
  );
}
