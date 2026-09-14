CREATE TABLE limit_policies (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('instance', 'user', 'key', 'connection')),
    scope_id TEXT NOT NULL DEFAULT '',
    metric TEXT NOT NULL CHECK (metric IN ('requests', 'tokens', 'spend', 'concurrency', 'body_bytes', 'output_tokens', 'batch_items')),
    algorithm TEXT NOT NULL CHECK (algorithm IN ('token_bucket', 'fixed_window', 'quota', 'concurrency', 'ceiling')),
    period TEXT NOT NULL DEFAULT '' CHECK (period IN ('', 'hour', 'day', 'week', 'month', 'lifetime')),
    window_seconds INTEGER NOT NULL DEFAULT 0 CHECK (window_seconds >= 0),
    limit_units INTEGER NOT NULL CHECK (limit_units > 0),
    refill_units INTEGER NOT NULL DEFAULT 0 CHECK (refill_units >= 0),
    refill_interval_ms INTEGER NOT NULL DEFAULT 0 CHECK (refill_interval_ms >= 0),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK ((scope_kind = 'instance' AND scope_id = '') OR (scope_kind <> 'instance' AND scope_id <> '')),
    CHECK (
        (algorithm = 'token_bucket' AND period = '' AND window_seconds = 0 AND refill_units > 0 AND refill_interval_ms > 0 AND metric IN ('requests', 'tokens')) OR
        (algorithm = 'fixed_window' AND period = '' AND window_seconds > 0 AND refill_units = 0 AND refill_interval_ms = 0 AND metric IN ('requests', 'tokens')) OR
        (algorithm = 'quota' AND period <> '' AND window_seconds = 0 AND refill_units = 0 AND refill_interval_ms = 0 AND metric IN ('requests', 'tokens', 'spend')) OR
        (algorithm = 'concurrency' AND period = '' AND window_seconds = 0 AND refill_units = 0 AND refill_interval_ms = 0 AND metric = 'concurrency') OR
        (algorithm = 'ceiling' AND period = '' AND window_seconds = 0 AND refill_units = 0 AND refill_interval_ms = 0 AND metric IN ('body_bytes', 'output_tokens', 'batch_items'))
    ),
    UNIQUE (scope_kind, scope_id, metric, algorithm, period, window_seconds)
);

CREATE INDEX limit_policies_scope ON limit_policies(scope_kind, scope_id, enabled);

CREATE TABLE bucket_state (
    policy_id TEXT PRIMARY KEY REFERENCES limit_policies(id) ON DELETE CASCADE,
    remaining_units INTEGER NOT NULL,
    refill_remainder INTEGER NOT NULL DEFAULT 0 CHECK (refill_remainder >= 0),
    last_refill_at INTEGER NOT NULL,
    last_effective_at INTEGER NOT NULL
);

CREATE TABLE quota_periods (
    policy_id TEXT NOT NULL REFERENCES limit_policies(id) ON DELETE RESTRICT,
    period_start INTEGER NOT NULL,
    period_end INTEGER,
    consumed_units INTEGER NOT NULL DEFAULT 0,
    reserved_units INTEGER NOT NULL DEFAULT 0,
    last_effective_at INTEGER NOT NULL,
    PRIMARY KEY (policy_id, period_start),
    CHECK (consumed_units >= 0),
    CHECK (reserved_units >= 0)
);

CREATE TABLE admission_clock (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_effective_at INTEGER NOT NULL,
    process_epoch TEXT NOT NULL
);

CREATE TABLE requests (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL,
    dialect TEXT NOT NULL,
    model_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('reserved', 'in_progress', 'succeeded', 'failed', 'cancelled', 'interrupted_unknown')),
    started_at INTEGER NOT NULL,
    finished_at INTEGER
);

CREATE INDEX requests_owner_time ON requests(owner_user_id, started_at DESC, id DESC);
CREATE INDEX requests_key_time ON requests(key_id, started_at DESC, id DESC);

CREATE TABLE price_versions (
    id TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    input_nanos_per_million INTEGER NOT NULL CHECK (input_nanos_per_million >= 0),
    output_nanos_per_million INTEGER NOT NULL CHECK (output_nanos_per_million >= 0),
    source TEXT NOT NULL,
    effective_from INTEGER NOT NULL,
    effective_to INTEGER,
    created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    CHECK (effective_to IS NULL OR effective_to > effective_from)
);

CREATE INDEX price_versions_lookup ON price_versions(connection_id, model_id, effective_from DESC);

CREATE TABLE attempts (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    connection_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    price_version_id TEXT REFERENCES price_versions(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('reserved', 'dispatching', 'streaming', 'succeeded', 'failed', 'cancelled_before_dispatch', 'cancelled', 'interrupted_unknown')),
    usage_status TEXT NOT NULL DEFAULT 'pending' CHECK (usage_status IN ('pending', 'provider_reported', 'estimated', 'unknown')),
    estimated_tokens INTEGER NOT NULL DEFAULT 0 CHECK (estimated_tokens >= 0),
    input_tokens INTEGER,
    output_tokens INTEGER,
    estimated_cost_nanos INTEGER,
    as_recorded_cost_nanos INTEGER,
    restated_cost_nanos INTEGER,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    UNIQUE (request_id, ordinal),
    CHECK (input_tokens IS NULL OR input_tokens >= 0),
    CHECK (output_tokens IS NULL OR output_tokens >= 0),
    CHECK (estimated_cost_nanos IS NULL OR estimated_cost_nanos >= 0),
    CHECK (as_recorded_cost_nanos IS NULL OR as_recorded_cost_nanos >= 0),
    CHECK (restated_cost_nanos IS NULL OR restated_cost_nanos >= 0)
);

CREATE INDEX attempts_request ON attempts(request_id, ordinal);
CREATE INDEX attempts_unresolved ON attempts(usage_status, started_at, id);

CREATE TABLE reservations (
    id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    policy_id TEXT NOT NULL REFERENCES limit_policies(id) ON DELETE RESTRICT,
    period_start INTEGER,
    reserved_units INTEGER NOT NULL CHECK (reserved_units >= 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'settled', 'released', 'uncertain')),
    created_at INTEGER NOT NULL,
    settled_at INTEGER,
    UNIQUE (attempt_id, policy_id)
);

CREATE INDEX reservations_active ON reservations(state, attempt_id);

CREATE TABLE concurrency_leases (
    policy_id TEXT NOT NULL REFERENCES limit_policies(id) ON DELETE RESTRICT,
    lease_kind TEXT NOT NULL CHECK (lease_kind IN ('request', 'attempt')),
    lease_id TEXT NOT NULL,
    request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE RESTRICT,
    process_epoch TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (policy_id, lease_kind, lease_id),
    CHECK ((lease_kind = 'request' AND attempt_id IS NULL AND lease_id = request_id) OR (lease_kind = 'attempt' AND attempt_id IS NOT NULL AND lease_id = attempt_id))
);

CREATE INDEX concurrency_leases_epoch ON concurrency_leases(process_epoch);

CREATE TABLE usage_ledger (
    id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    entry_type TEXT NOT NULL CHECK (entry_type IN ('settlement', 'reconciliation', 'adjustment', 'reprice')),
    token_units INTEGER,
    cost_nanos INTEGER,
    idempotency_key TEXT NOT NULL UNIQUE,
    actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE cost_assessments (
    id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    price_version_id TEXT NOT NULL REFERENCES price_versions(id) ON DELETE RESTRICT,
    calculation_version INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('recorded', 'restated')),
    amount_nanos INTEGER NOT NULL CHECK (amount_nanos >= 0),
    delta_nanos INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (attempt_id, price_version_id, calculation_version, kind)
);

CREATE TABLE pricing_jobs (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    model_id TEXT NOT NULL,
    connection_id TEXT NOT NULL,
    from_time INTEGER NOT NULL,
    to_time INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('preview', 'completed', 'failed')),
    affected_attempts INTEGER NOT NULL DEFAULT 0,
    missing_prices INTEGER NOT NULL DEFAULT 0,
    delta_nanos INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    completed_at INTEGER
);

CREATE TABLE event_outbox (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    request_id TEXT,
    attempt_id TEXT,
    payload_json TEXT NOT NULL CHECK (length(payload_json) <= 16384),
    created_at INTEGER NOT NULL,
    delivered_at INTEGER
);

CREATE INDEX event_outbox_pending ON event_outbox(delivered_at, sequence);
