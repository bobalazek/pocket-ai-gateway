CREATE TABLE stored_responses (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    body_json BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX stored_responses_owner ON stored_responses(owner_user_id, created_at DESC, id DESC);
CREATE INDEX stored_responses_key ON stored_responses(key_id, created_at DESC, id DESC);
CREATE INDEX stored_responses_expiry ON stored_responses(expires_at);
