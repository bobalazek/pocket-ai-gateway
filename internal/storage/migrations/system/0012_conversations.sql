CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    key_id TEXT NOT NULL REFERENCES api_keys(id),
    metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json)),
    created_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE INDEX conversations_key ON conversations(key_id, created_at DESC, id DESC);

CREATE TABLE conversation_items (
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    id TEXT PRIMARY KEY,
    ordinal INTEGER NOT NULL,
    body_json BLOB NOT NULL CHECK (json_valid(body_json)),
    created_at INTEGER NOT NULL,
    UNIQUE(conversation_id, ordinal)
);

CREATE INDEX conversation_items_order ON conversation_items(conversation_id, ordinal);
