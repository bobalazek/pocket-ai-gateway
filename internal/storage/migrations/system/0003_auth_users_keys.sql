ALTER TABLE users ADD COLUMN auth_revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN inference_unrestricted INTEGER NOT NULL DEFAULT 0 CHECK (inference_unrestricted IN (0, 1));
ALTER TABLE users ADD COLUMN scopes_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN model_patterns_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN connection_ids_json TEXT NOT NULL DEFAULT '[]';
UPDATE users SET inference_unrestricted = 1 WHERE role = 'owner';

CREATE TABLE user_sessions (
    id TEXT PRIMARY KEY,
    verifier BLOB NOT NULL UNIQUE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    authenticated_at INTEGER NOT NULL,
    auth_revision INTEGER NOT NULL,
    user_agent TEXT NOT NULL DEFAULT ''
);

INSERT INTO user_sessions (id, verifier, user_id, expires_at, created_at, last_seen_at, authenticated_at, auth_revision)
SELECT 'ses_' || lower(hex(randomblob(16))), verifier, user_id, expires_at, created_at, created_at, created_at, 1 FROM sessions;

DROP TABLE sessions;
CREATE INDEX user_sessions_user_id ON user_sessions(user_id, created_at DESC);

CREATE TABLE activation_tokens (
    verifier BLOB PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('activation', 'recovery')),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX activation_tokens_user_id ON activation_tokens(user_id);

CREATE TABLE login_throttles (
    subject BLOB PRIMARY KEY,
    window_started_at INTEGER NOT NULL,
    failures INTEGER NOT NULL,
    blocked_until INTEGER NOT NULL
);

CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    label TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'disabled', 'revoked')),
    scopes_json TEXT NOT NULL,
    model_patterns_json TEXT NOT NULL DEFAULT '[]',
    connection_ids_json TEXT NOT NULL DEFAULT '[]',
    expires_at INTEGER,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX api_keys_owner ON api_keys(owner_user_id, created_at DESC, id DESC);

CREATE TABLE api_key_secrets (
    selector TEXT PRIMARY KEY,
    verifier BLOB NOT NULL,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER
);

CREATE INDEX api_key_secrets_key ON api_key_secrets(key_id);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);

CREATE INDEX audit_events_created ON audit_events(created_at DESC, id DESC);
