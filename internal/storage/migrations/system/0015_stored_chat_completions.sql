ALTER TABLE stored_responses ADD COLUMN request_id TEXT REFERENCES requests(id) ON DELETE SET NULL;
CREATE INDEX stored_responses_request ON stored_responses(request_id, state);

CREATE TABLE stored_chat_completions (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    body_json BLOB NOT NULL,
    request_json BLOB NOT NULL,
    metadata_json BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX stored_chat_completions_key ON stored_chat_completions(key_id, created_at, id);
CREATE INDEX stored_chat_completions_expiry ON stored_chat_completions(expires_at);

CREATE TABLE stored_chat_completion_metadata (
    completion_id TEXT NOT NULL REFERENCES stored_chat_completions(id) ON DELETE CASCADE,
    metadata_key TEXT NOT NULL,
    metadata_value TEXT NOT NULL,
    PRIMARY KEY (completion_id, metadata_key)
);

CREATE INDEX stored_chat_completion_metadata_filter ON stored_chat_completion_metadata(metadata_key, metadata_value, completion_id);
