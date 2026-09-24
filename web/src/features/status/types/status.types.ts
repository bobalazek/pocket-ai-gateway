import type { OutboxStatus } from "@/features/usage/types/usage.types";

export type RuntimeCheck = { id: "system_database" | "data_database" | "usage_projection"; state: "ready" | "unavailable" };
export type RuntimeAlert = { id: string; severity: "warning" | "critical"; title: string; description: string };
export type OperationalStatus = { ready: boolean; checked_at: string; checks: RuntimeCheck[]; outbox: OutboxStatus | null; alerts: RuntimeAlert[] };
