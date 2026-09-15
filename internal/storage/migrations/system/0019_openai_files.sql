CREATE TABLE openai_files (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    filename TEXT NOT NULL CHECK (length(CAST(filename AS BLOB)) BETWEEN 1 AND 512 AND instr(filename, char(0)) = 0),
    purpose TEXT NOT NULL CHECK (purpose IN ('batch', 'batch_output')),
    bytes INTEGER NOT NULL CHECK (bytes BETWEEN 0 AND 16777216),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) = bytes + 16),
    nonce BLOB NOT NULL CHECK (length(nonce) = 12),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at BETWEEN created_at + 3600000 AND created_at + 2592000000)
);

CREATE INDEX openai_files_owner ON openai_files(owner_user_id, created_at DESC, id DESC);
CREATE INDEX openai_files_key ON openai_files(key_id, created_at DESC, id DESC);
CREATE INDEX openai_files_expiry ON openai_files(expires_at);
