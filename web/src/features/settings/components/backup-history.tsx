import { Card } from "@/components/ui/card";
import { formatBytes } from "@/features/settings/components/settings-fields";
import type { useSettings } from "@/features/settings/hooks/use-settings";

type SettingsModel = ReturnType<typeof useSettings>;

export function BackupHistory({ model }: { model: SettingsModel }) {
  return (
    <section className="section-block">
      <div className="section-heading"><div><h2>Backup history</h2><p className="fine-print">Checksums verify the encrypted archive as stored.</p></div></div>
      {model.backups.length ? (
        <div className="resource-list">
          {model.backups.map((backup) => (
            <Card className="resource-row" key={backup.id}>
              <div><strong>{backup.archive_name}</strong><small>{new Date(backup.started_at).toLocaleString()} · {backup.destination} · {backup.state} · {formatBytes(backup.size_bytes)}</small>{backup.checksum && <code>{backup.checksum}</code>}{backup.error && <small className="form-error">{backup.error}</small>}</div>
            </Card>
          ))}
        </div>
      ) : <p className="empty-copy">No backup jobs yet.</p>}
    </section>
  );
}
