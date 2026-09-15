"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { BackupJob, ConfigPreview, Diagnostics, OperationSettings } from "@/features/settings/types/settings.types";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function useSettings() {
	const user = useGatewayUser();
	const isOwner = user?.role === "owner";
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
      const [settingsResult, backupResult, diagnosticResult] = await Promise.all([pocketAIGatewayAdmin.settings.get(), pocketAIGatewayAdmin.settings.backups(), pocketAIGatewayAdmin.settings.diagnostics()]);
      setSettings(settingsResult.settings);
		setBackups(backupResult.data ?? []);
      setDiagnostics(diagnosticResult.diagnostics);
      setError("");
    } catch (failure) {
      setError(message(failure, "Operations are unavailable"));
    }
  }

  useEffect(() => { if (isOwner) void load(); }, [isOwner]);

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
      const result = await pocketAIGatewayAdmin.settings.update(value);
      setSettings(result.settings);
      setNotice("Settings saved.");
      setError("");
    } catch (failure) { setError(message(failure, "Settings could not be saved")); }
    finally { setBusy(false); }
  }

  async function createBackup() {
    setBusy(true);
    try { await pocketAIGatewayAdmin.settings.runBackup(); setNotice("Encrypted backup created and verified."); await load(); }
    catch (failure) { setError(message(failure, "Backup could not be created")); }
    finally { setBusy(false); }
  }

  async function retain() {
		if (!window.confirm("Delete expired projected usage events and audit records using the configured retention periods?")) return;
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.settings.runRetention();
      const total = Object.values(result.deleted).reduce((sum, count) => sum + count, 0);
      setNotice(`${total.toLocaleString()} expired details and audit records processed.`);
      setError("");
    } catch (failure) { setError(message(failure, "Retention could not run")); }
    finally { setBusy(false); }
  }

  async function downloadConfig() {
    setBusy(true);
    try {
      const bundle = await pocketAIGatewayAdmin.settings.exportConfig();
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
      const result = await pocketAIGatewayAdmin.settings.previewConfig(bundle);
			if (generation !== previewGeneration.current) return;
			setPendingConfig(bundle); setPendingFile(`${file.name} (${file.size.toLocaleString()} bytes)`); setPreview(result.preview); setError("");
		} catch (failure) { if (generation === previewGeneration.current) { setPendingConfig(null); setPendingFile(""); setPreview(null); setError(message(failure, "Configuration is invalid")); } }
    finally { if (generation === previewGeneration.current) setBusy(false); }
  }

  async function applyConfig() {
    if (!pendingConfig || !preview) return;
		if (!window.confirm(`Apply the reviewed configuration from ${pendingFile}?`)) return;
    setBusy(true);
		try { await pocketAIGatewayAdmin.settings.importConfig(pendingConfig); setPendingConfig(null); setPendingFile(""); setPreview(null); setNotice("Configuration imported. Credentials for changed provider endpoints must be added again."); await load(); }
    catch (failure) { setError(message(failure, "Configuration could not be imported")); }
    finally { setBusy(false); }
  }

  return { isOwner, settings, backups, diagnostics, pendingFile, preview, busy, error, notice, previewGeneration, save, createBackup, retain, downloadConfig, chooseConfig, applyConfig, setPendingConfig, setPendingFile, setPreview };
}

function message(error: unknown, fallback: string) { return error instanceof GatewayAPIError ? error.message : error instanceof Error ? error.message : fallback; }
