"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { AppShell } from "@/components/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GatewayAPIError, gatewayAPI, type BackupJob, type ConfigPreview, type Diagnostics, type OperationSettings } from "@/lib/api-client";

export default function SettingsPage() {
	const previewGeneration = useRef(0);
  const [settings, setSettings] = useState<OperationSettings | null>(null);
  const [backups, setBackups] = useState<BackupJob[]>([]);
  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null);
  const [pendingConfig, setPendingConfig] = useState<unknown>(null);
	const [pendingFile, setPendingFile] = useState("");
  const [preview, setPreview] = useState<ConfigPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function load() {
    try {
      const [settingsResult, backupResult, diagnosticResult] = await Promise.all([gatewayAPI.operationSettings(), gatewayAPI.backups(), gatewayAPI.diagnostics()]);
      setSettings(settingsResult.settings);
		setBackups(backupResult.data ?? []);
      setDiagnostics(diagnosticResult.diagnostics);
      setError("");
    } catch (failure) {
      setError(message(failure, "Operations are unavailable"));
    }
  }

  useEffect(() => { void load(); }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!settings) return;
    const form = new FormData(event.currentTarget);
    const value: OperationSettings = {
      ...settings,
      backup_enabled: form.get("backup_enabled") === "enabled",
      backup_interval_hours: Number(form.get("backup_interval_hours")),
      backup_retention_count: Number(form.get("backup_retention_count")),
      backup_destination: String(form.get("backup_destination")) as "local" | "s3",
      local_directory: String(form.get("local_directory")),
      s3_endpoint: String(form.get("s3_endpoint")),
      s3_region: String(form.get("s3_region")),
      s3_bucket: String(form.get("s3_bucket")),
      s3_prefix: String(form.get("s3_prefix")),
      s3_access_key_env: String(form.get("s3_access_key_env")),
      s3_secret_key_env: String(form.get("s3_secret_key_env")),
      request_retention_days: Number(form.get("request_retention_days")),
      audit_retention_days: Number(form.get("audit_retention_days")),
    };
    setBusy(true);
    try {
      const result = await gatewayAPI.updateOperationSettings(value);
      setSettings(result.settings);
      setNotice("Settings saved.");
      setError("");
    } catch (failure) { setError(message(failure, "Settings could not be saved")); }
    finally { setBusy(false); }
  }

  async function createBackup() {
    setBusy(true);
    try { await gatewayAPI.runBackup(); setNotice("Encrypted backup created and verified."); await load(); }
    catch (failure) { setError(message(failure, "Backup could not be created")); }
    finally { setBusy(false); }
  }

  async function retain() {
		if (!window.confirm("Delete expired projected usage events and audit records using the configured retention periods?")) return;
    setBusy(true);
    try {
      const result = await gatewayAPI.runRetention();
      const total = Object.values(result.deleted).reduce((sum, count) => sum + count, 0);
      setNotice(`${total.toLocaleString()} expired details and audit records processed.`);
      setError("");
    } catch (failure) { setError(message(failure, "Retention could not run")); }
    finally { setBusy(false); }
  }

  async function downloadConfig() {
    setBusy(true);
    try {
      const bundle = await gatewayAPI.exportConfig();
      const url = URL.createObjectURL(new Blob([JSON.stringify(bundle, null, 2) + "\n"], { type: "application/json" }));
      const link = document.createElement("a");
      link.href = url; link.download = "pocket-ai-gateway-config.json"; link.click(); URL.revokeObjectURL(url);
      setNotice("Portable configuration exported without credentials, users, sessions, or API keys.");
    } catch (failure) { setError(message(failure, "Configuration could not be exported")); }
    finally { setBusy(false); }
  }

  async function chooseConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
		setPendingConfig(null); setPendingFile(""); setPreview(null);
    const file = (new FormData(event.currentTarget).get("config") as File | null);
    if (!file || file.size > 1 << 20) { setError("Choose a configuration file no larger than 1 MiB."); return; }
		const generation = previewGeneration.current;
    setBusy(true);
    try {
      const bundle: unknown = JSON.parse(await file.text());
      const result = await gatewayAPI.previewConfig(bundle);
			if (generation !== previewGeneration.current) return;
			setPendingConfig(bundle); setPendingFile(`${file.name} (${file.size.toLocaleString()} bytes)`); setPreview(result.preview); setError("");
		} catch (failure) { if (generation === previewGeneration.current) { setPendingConfig(null); setPendingFile(""); setPreview(null); setError(message(failure, "Configuration is invalid")); } }
    finally { if (generation === previewGeneration.current) setBusy(false); }
  }

  async function applyConfig() {
    if (!pendingConfig || !preview) return;
		if (!window.confirm(`Apply the reviewed configuration from ${pendingFile}?`)) return;
    setBusy(true);
		try { await gatewayAPI.importConfig(pendingConfig); setPendingConfig(null); setPendingFile(""); setPreview(null); setNotice("Configuration imported. Credentials for changed provider endpoints must be added again."); await load(); }
    catch (failure) { setError(message(failure, "Configuration could not be imported")); }
    finally { setBusy(false); }
  }

  return <AppShell active="Settings"><main id="main-content" className="content management-page">
    <header className="page-header"><div><h1>Settings and recovery</h1><p className="lede">Encrypted backups, portable configuration, retention, and safe runtime details.</p></div></header>
    {error && <p className="form-error" role="alert">{error}</p>}{notice && <p className="form-success" role="status">{notice}</p>}
    {settings && <form key={settings.revision} onSubmit={save}>
      <section className="settings-grid">
        <Card className="panel"><h2>Backup schedule</h2><p className="fine-print">Archives contain both databases and the provider credential key. Set <code>POCKET_AI_GATEWAY_BACKUP_KEY</code> to 32 random bytes encoded as base64.</p><div className="field"><Label htmlFor="backup_enabled">Schedule</Label><select className="select" id="backup_enabled" name="backup_enabled" defaultValue={settings.backup_enabled ? "enabled" : "disabled"}><option value="disabled">Disabled</option><option value="enabled">Enabled</option></select></div><Fields values={settings} names={["backup_interval_hours", "backup_retention_count", "local_directory"]}/><div className="field"><Label htmlFor="backup_destination">Destination</Label><select className="select" id="backup_destination" name="backup_destination" defaultValue={settings.backup_destination}><option value="local">Local directory</option><option value="s3">S3-compatible storage</option></select></div><p className={settings.backup_key_configured ? "form-success" : "form-error"}>{settings.backup_key_configured ? "Backup encryption key configured." : "Backup encryption key is missing or invalid."}</p></Card>
        <Card className="panel"><h2>S3-compatible storage</h2><p className="fine-print">Credential values stay in environment variables. Only their names are saved.</p><Fields values={settings} names={["s3_endpoint", "s3_region", "s3_bucket", "s3_prefix", "s3_access_key_env", "s3_secret_key_env"]}/></Card>
		<Card className="panel"><h2>Retention</h2><p className="fine-print">Projected event detail and old audit events are removed. Authoritative accounting, active reservations, and enforcement counters remain.</p><Fields values={settings} names={["request_retention_days", "audit_retention_days"]}/><Button type="button" variant="outline" disabled={busy} onClick={retain}>Run retention now</Button></Card>
        <Card className="panel"><h2>Runtime</h2>{diagnostics ? <dl className="facts compact"><Fact label="Version" value={diagnostics.version}/><Fact label="SQLite" value={diagnostics.sqlite_version}/><Fact label="Uptime" value={`${Math.floor(diagnostics.uptime_seconds / 60)} min`}/><Fact label="Database size" value={formatBytes(diagnostics.system_database_bytes + diagnostics.data_database_bytes)}/><Fact label="Pending projection events" value={diagnostics.pending_outbox_events.toLocaleString()}/></dl> : <p>Loading diagnostics…</p>}</Card>
      </section>
      <div className="row-actions section-block"><Button disabled={busy}>Save settings</Button><Button type="button" variant="outline" disabled={busy || !settings.backup_key_configured} onClick={createBackup}>Create backup now</Button></div>
    </form>}
    <section className="section-block"><div className="section-heading"><div><h2>Backup history</h2><p className="fine-print">Checksums verify the encrypted archive as stored.</p></div></div>{backups.length ? <div className="resource-list">{backups.map((backup) => <Card className="resource-row" key={backup.id}><div><strong>{backup.archive_name}</strong><small>{new Date(backup.started_at).toLocaleString()} · {backup.destination} · {backup.state} · {formatBytes(backup.size_bytes)}</small>{backup.checksum && <code>{backup.checksum}</code>}{backup.error && <small className="form-error">{backup.error}</small>}</div></Card>)}</div> : <p className="empty-copy">No backup jobs yet.</p>}</section>
		<section className="settings-grid section-block"><Card className="panel"><h2>Export configuration</h2><p>Download providers, models, routes, policies, prices, catalog, and operations settings. Secrets and identities are excluded.</p><Button type="button" variant="outline" disabled={busy} onClick={downloadConfig}>Export JSON</Button></Card><Card className="panel"><h2>Import configuration</h2><form onSubmit={chooseConfig}><div className="field"><Label htmlFor="config">Configuration JSON</Label><Input id="config" name="config" type="file" accept="application/json,.json" required disabled={busy} onChange={() => { previewGeneration.current++; setPendingConfig(null); setPendingFile(""); setPreview(null); }} /></div>{preview && <p className="form-success" role="status">Ready from {pendingFile}: {preview.connections} providers, {preview.public_models} models, {preview.policies} policies, and {preview.prices} prices. Credentials stay local; changed endpoints require credentials again.</p>}<div className="row-actions"><Button disabled={busy}>{preview ? "Preview another" : "Preview import"}</Button>{preview && <Button type="button" disabled={busy} onClick={applyConfig}>Apply {pendingFile}</Button>}</div></form></Card></section>
  </main></AppShell>;
}

const labels: Record<string, string> = { backup_interval_hours: "Interval (hours)", backup_retention_count: "Local archives to keep", local_directory: "Local backup directory", s3_endpoint: "Endpoint", s3_region: "Region", s3_bucket: "Bucket", s3_prefix: "Object prefix", s3_access_key_env: "Access key environment name", s3_secret_key_env: "Secret key environment name", request_retention_days: "Projected event detail (days)", audit_retention_days: "Audit events (days)" };
function Fields({ values, names }: { values: OperationSettings; names: (keyof OperationSettings)[] }) { return <>{names.map((name) => <div className="field" key={name}><Label htmlFor={name}>{labels[name]}</Label><Input id={name} name={name} type={typeof values[name] === "number" ? "number" : "text"} min={name === "audit_retention_days" ? 30 : 1} defaultValue={String(values[name])} /></div>)}</>; }
function Fact({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function formatBytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KiB` : `${(value / 1024 / 1024).toFixed(1)} MiB`; }
function message(error: unknown, fallback: string) { return error instanceof GatewayAPIError ? error.message : error instanceof Error ? error.message : fallback; }
