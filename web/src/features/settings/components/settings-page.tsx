"use client";

import { AppShell } from "@/components/app-shell";
import { BackupHistory } from "@/features/settings/components/backup-history";
import { ConfigTransfer } from "@/features/settings/components/config-transfer";
import { OperationsSettings } from "@/features/settings/components/operations-settings";
import { useSettings } from "@/features/settings/hooks/use-settings";

export default function SettingsPage() {
  const model = useSettings();
  if (!model.isOwner) return <AppShell active="Settings"><main id="main-content" className="content"><p className="context">Access denied</p><h1>Owner access required.</h1></main></AppShell>;
  return <AppShell active="Settings"><main id="main-content" className="content management-page">
    <header className="page-header"><div><h1>Settings and recovery</h1><p className="lede">Encrypted backups, portable configuration, retention, and safe runtime details.</p></div></header>
    {model.error && <p className="form-error" role="alert">{model.error}</p>}{model.notice && <p className="form-success" role="status">{model.notice}</p>}
    <OperationsSettings model={model}/>
    <BackupHistory model={model}/>
    <ConfigTransfer model={model}/>
  </main></AppShell>;
}
