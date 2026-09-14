CREATE TABLE provider_connections (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    adapter TEXT NOT NULL CHECK (adapter IN ('openai', 'anthropic', 'gemini', 'openai_compatible')),
    base_url TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    allow_private_network INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_network IN (0, 1)),
    timeout_ms INTEGER NOT NULL DEFAULT 60000 CHECK (timeout_ms BETWEEN 1000 AND 600000),
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE provider_credentials (
    connection_id TEXT PRIMARY KEY REFERENCES provider_connections(id) ON DELETE CASCADE,
    ciphertext BLOB,
    nonce BLOB,
    external_ref TEXT,
    updated_at INTEGER NOT NULL,
    CHECK ((ciphertext IS NOT NULL AND nonce IS NOT NULL AND external_ref IS NULL) OR (ciphertext IS NULL AND nonce IS NULL AND external_ref IS NOT NULL))
);

CREATE TABLE upstream_models (
    id TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL REFERENCES provider_connections(id) ON DELETE RESTRICT,
    upstream_id TEXT NOT NULL,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (connection_id, upstream_id)
);

CREATE INDEX upstream_models_connection ON upstream_models(connection_id, active, upstream_id);

CREATE TABLE public_models (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    target_connection_id TEXT NOT NULL REFERENCES provider_connections(id) ON DELETE RESTRICT,
    target_model_id TEXT NOT NULL REFERENCES upstream_models(id) ON DELETE RESTRICT,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX public_models_active ON public_models(active, id);
ALTER TABLE attempts ADD COLUMN upstream_model_record_id TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN upstream_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN connection_revision INTEGER NOT NULL DEFAULT 0;
CREATE INDEX attempts_request_history ON attempts(started_at DESC, id DESC);
