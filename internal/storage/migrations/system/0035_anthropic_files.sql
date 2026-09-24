CREATE TABLE anthropic_files (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    filename TEXT NOT NULL CHECK (length(CAST(filename AS BLOB)) BETWEEN 1 AND 255),
    mime_type TEXT NOT NULL CHECK (mime_type IN ('application/pdf', 'text/plain', 'image/jpeg', 'image/png', 'image/gif', 'image/webp')),
    size_bytes INTEGER NOT NULL CHECK (size_bytes BETWEEN 1 AND 8388608),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) = size_bytes + 16),
    nonce BLOB NOT NULL CHECK (length(nonce) = 12),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at BETWEEN created_at + 3600000 AND created_at + 7776000000)
);

CREATE INDEX anthropic_files_key ON anthropic_files(key_id, created_at DESC, id DESC);
CREATE INDEX anthropic_files_expiry ON anthropic_files(expires_at);
