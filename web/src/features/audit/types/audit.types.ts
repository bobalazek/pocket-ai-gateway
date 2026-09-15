export type AuditEvent = { id: string; actor_user_id?: string; action: string; resource_type: string; resource_id: string; detail_json: string; created_at: string };
export type AuditFilters = { actor_user_id?: string; action?: string; resource_type?: string; from?: string; to?: string; cursor?: string };
