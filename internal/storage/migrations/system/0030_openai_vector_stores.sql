CREATE TABLE openai_vector_stores (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '' CHECK (length(CAST(name AS BLOB)) <= 256 AND instr(name, char(0)) = 0),
    description TEXT NOT NULL DEFAULT '' CHECK (length(CAST(description AS BLOB)) <= 4096 AND instr(description, char(0)) = 0),
    metadata_json BLOB NOT NULL DEFAULT '{}' CHECK (length(metadata_json) BETWEEN 2 AND 16384 AND json_valid(metadata_json) AND json_type(metadata_json) = 'object'),
    created_at INTEGER NOT NULL,
    last_active_at INTEGER NOT NULL CHECK (last_active_at >= created_at),
    expires_after_days INTEGER CHECK (expires_after_days BETWEEN 1 AND 365),
    expires_at INTEGER,
    CHECK ((expires_after_days IS NULL AND expires_at IS NULL) OR (expires_after_days IS NOT NULL AND expires_at = last_active_at + expires_after_days * 86400000))
);

CREATE INDEX openai_vector_stores_owner ON openai_vector_stores(owner_user_id, created_at DESC, id DESC);
CREATE INDEX openai_vector_stores_key ON openai_vector_stores(key_id, created_at DESC, id DESC);
CREATE INDEX openai_vector_stores_expiry ON openai_vector_stores(expires_at) WHERE expires_at IS NOT NULL;
