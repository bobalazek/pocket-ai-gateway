import type { BackupJob, ConfigPreview, Diagnostics, OperationSettings } from "@/features/settings/types/settings.types";
import { gatewayTransport } from "@/lib/api-client";

export const settingsClient = {
  get: () => gatewayTransport.request<{ settings: OperationSettings }>("/api/v1/admin/settings"),
  update: (settings: OperationSettings) => gatewayTransport.request<{ settings: OperationSettings }>("/api/v1/admin/settings", { method: "PATCH", revision: settings.revision, body: settings }),
  backups: () => gatewayTransport.request<{ data: BackupJob[] }>("/api/v1/admin/backups"),
  runBackup: () => gatewayTransport.request<{ backup: BackupJob }>("/api/v1/admin/backups", { method: "POST" }),
  runRetention: () => gatewayTransport.request<{ deleted: Record<string, number> }>("/api/v1/admin/retention", { method: "POST" }),
  diagnostics: () => gatewayTransport.request<{ diagnostics: Diagnostics }>("/api/v1/admin/diagnostics"),
  exportConfig: () => gatewayTransport.request<unknown>("/api/v1/admin/config/export"),
  previewConfig: (bundle: unknown) => gatewayTransport.request<{ preview: ConfigPreview }>("/api/v1/admin/config/preview", { method: "POST", body: bundle }),
  importConfig: (bundle: unknown) => gatewayTransport.request<{ imported: ConfigPreview }>("/api/v1/admin/config/import", { method: "POST", body: bundle }),
};
