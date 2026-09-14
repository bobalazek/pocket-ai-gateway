CREATE TABLE usage_events (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    request_id TEXT,
    attempt_id TEXT,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX usage_events_created ON usage_events(created_at DESC, event_id DESC);

CREATE TABLE usage_daily (
    date TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    key_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    connection_id TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    known_cost_nanos INTEGER NOT NULL DEFAULT 0,
    unknown_attempts INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (date, owner_user_id, key_id, model_id, connection_id)
);
