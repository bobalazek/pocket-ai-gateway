import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Fact, formatBytes, SettingsFields } from "@/features/settings/components/settings-fields";
import type { useSettings } from "@/features/settings/hooks/use-settings";

type SettingsModel = ReturnType<typeof useSettings>;

export function OperationsSettings({ model }: { model: SettingsModel }) {
  const { settings, diagnostics, busy, save, createBackup, retain } = model;
  if (!settings) return null;

  return (
    <form key={settings.revision} onSubmit={save}>
      <section className="settings-grid">
        <Card className="panel">
          <h2>Backup schedule</h2>
          <p className="fine-print">Archives contain both databases and the provider credential key. Set <code>POCKET_AI_GATEWAY_BACKUP_KEY</code> to 32 random bytes encoded as base64.</p>
          <div className="field"><Label htmlFor="backup_enabled">Schedule</Label><select className="select" id="backup_enabled" name="backup_enabled" defaultValue={settings.backup_enabled ? "enabled" : "disabled"}><option value="disabled">Disabled</option><option value="enabled">Enabled</option></select></div>
          <SettingsFields values={settings} names={["backup_interval_hours", "backup_retention_count", "local_directory"]} />
          <div className="field"><Label htmlFor="backup_destination">Destination</Label><select className="select" id="backup_destination" name="backup_destination" defaultValue={settings.backup_destination}><option value="local">Local directory</option><option value="s3">S3-compatible storage</option></select></div>
          <p className={settings.backup_key_configured ? "form-success" : "form-error"}>{settings.backup_key_configured ? "Backup encryption key configured." : "Backup encryption key is missing or invalid."}</p>
        </Card>
        <Card className="panel">
          <h2>S3-compatible storage</h2>
          <p className="fine-print">Credential values stay in environment variables. Only their names are saved.</p>
          <SettingsFields values={settings} names={["s3_endpoint", "s3_region", "s3_bucket", "s3_prefix", "s3_access_key_env", "s3_secret_key_env"]} />
        </Card>
        <Card className="panel">
          <h2>Retention</h2>
          <p className="fine-print">Projected event detail and old audit events are removed. Authoritative accounting, active reservations, and enforcement counters remain.</p>
          <SettingsFields values={settings} names={["request_retention_days", "audit_retention_days"]} />
          <Button type="button" variant="outline" disabled={busy} onClick={retain}>Run retention now</Button>
        </Card>
        <Card className="panel">
          <h2>Runtime</h2>
          {diagnostics ? <dl className="facts compact"><Fact label="Version" value={diagnostics.version} /><Fact label="SQLite" value={diagnostics.sqlite_version} /><Fact label="Uptime" value={`${Math.floor(diagnostics.uptime_seconds / 60)} min`} /><Fact label="Database size" value={formatBytes(diagnostics.system_database_bytes + diagnostics.data_database_bytes)} /><Fact label="Pending projection events" value={diagnostics.pending_outbox_events.toLocaleString()} /></dl> : <p>Loading diagnostics…</p>}
        </Card>
      </section>
      <div className="row-actions section-block"><Button disabled={busy}>Save settings</Button><Button type="button" variant="outline" disabled={busy || !settings.backup_key_configured} onClick={createBackup}>Create backup now</Button></div>
    </form>
  );
}
