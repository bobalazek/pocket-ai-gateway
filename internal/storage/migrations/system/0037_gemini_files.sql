CREATE TABLE gemini_files (
    id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (length(CAST(display_name AS BLOB)) BETWEEN 1 AND 512),
    mime_type TEXT NOT NULL CHECK (length(CAST(mime_type AS BLOB)) BETWEEN 3 AND 128),
    bytes INTEGER NOT NULL CHECK (bytes BETWEEN 1 AND 8388608),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) = bytes + 16),
    nonce BLOB NOT NULL CHECK (length(nonce) = 12),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at = created_at + 172800000),
    PRIMARY KEY (key_id, id)
);

CREATE INDEX gemini_files_key ON gemini_files(key_id, created_at DESC, id DESC);
CREATE INDEX gemini_files_expiry ON gemini_files(expires_at);

CREATE TABLE gemini_uploads (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (length(CAST(display_name AS BLOB)) BETWEEN 1 AND 512),
    mime_type TEXT NOT NULL CHECK (length(CAST(mime_type AS BLOB)) BETWEEN 3 AND 128),
    expected_bytes INTEGER NOT NULL CHECK (expected_bytes BETWEEN 1 AND 8388608),
    received_bytes INTEGER NOT NULL DEFAULT 0 CHECK (received_bytes BETWEEN 0 AND expected_bytes),
    requested_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed')),
    file_id TEXT,
    completed_at INTEGER,
    ciphertext BLOB,
    nonce BLOB,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at = created_at + 3600000),
    CHECK ((status = 'pending' AND file_id IS NULL AND completed_at IS NULL AND
           ((received_bytes = 0 AND ciphertext IS NULL AND nonce IS NULL) OR
            (received_bytes > 0 AND length(ciphertext) = received_bytes + 16 AND length(nonce) = 12))) OR
           (status = 'completed' AND file_id IS NOT NULL AND completed_at IS NOT NULL AND
            received_bytes = expected_bytes AND ciphertext IS NULL AND nonce IS NULL))
);

CREATE INDEX gemini_uploads_expiry ON gemini_uploads(expires_at);
