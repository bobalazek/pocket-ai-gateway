import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { OperationSettings } from "@/features/settings/types/settings.types";

const labels: Record<string, string> = {
  backup_interval_hours: "Interval (hours)",
  backup_retention_count: "Local archives to keep",
  local_directory: "Local backup directory",
  s3_endpoint: "Endpoint",
  s3_region: "Region",
  s3_bucket: "Bucket",
  s3_prefix: "Object prefix",
  s3_access_key_env: "Access key environment name",
  s3_secret_key_env: "Secret key environment name",
  request_retention_days: "Projected event detail (days)",
  audit_retention_days: "Audit events (days)",
};

export function SettingsFields({ values, names }: { values: OperationSettings; names: (keyof OperationSettings)[] }) {
  return <>{names.map((name) => (
    <div className="field" key={name}>
      <Label htmlFor={name}>{labels[name]}</Label>
      <Input id={name} name={name} type={typeof values[name] === "number" ? "number" : "text"} min={name === "audit_retention_days" ? 30 : 1} defaultValue={String(values[name])} />
    </div>
  ))}</>;
}

export function Fact({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>;
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
