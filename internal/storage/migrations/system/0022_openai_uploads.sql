CREATE TABLE openai_uploads (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    filename TEXT NOT NULL CHECK (length(CAST(filename AS BLOB)) BETWEEN 1 AND 512 AND instr(filename, char(0)) = 0),
    purpose TEXT NOT NULL CHECK (purpose = 'batch'),
    mime_type TEXT NOT NULL CHECK (length(CAST(mime_type AS BLOB)) BETWEEN 1 AND 128 AND instr(mime_type, char(0)) = 0),
    expected_bytes INTEGER NOT NULL CHECK (expected_bytes BETWEEN 1 AND 16777216),
    file_expiry_seconds INTEGER NOT NULL CHECK (file_expiry_seconds BETWEEN 3600 AND 2592000),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'cancelled')),
    file_id TEXT REFERENCES openai_files(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    completed_at INTEGER,
    cancelled_at INTEGER,
    CHECK (expires_at = created_at + 3600000),
    CHECK (
        (status = 'pending' AND file_id IS NULL AND completed_at IS NULL AND cancelled_at IS NULL) OR
        (status = 'completed' AND completed_at IS NOT NULL AND cancelled_at IS NULL) OR
        (status = 'cancelled' AND file_id IS NULL AND completed_at IS NULL AND cancelled_at IS NOT NULL)
    ),
    CHECK (completed_at IS NULL OR completed_at BETWEEN created_at AND expires_at),
    CHECK (cancelled_at IS NULL OR cancelled_at BETWEEN created_at AND expires_at)
);

CREATE INDEX openai_uploads_key ON openai_uploads(key_id, created_at DESC, id DESC);
CREATE INDEX openai_uploads_expiry ON openai_uploads(expires_at);

CREATE TABLE openai_upload_parts (
    id TEXT PRIMARY KEY,
    upload_id TEXT NOT NULL REFERENCES openai_uploads(id) ON DELETE CASCADE,
    bytes INTEGER NOT NULL CHECK (bytes BETWEEN 1 AND 16777216),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) = bytes + 16),
    nonce BLOB NOT NULL CHECK (length(nonce) = 12),
    created_at INTEGER NOT NULL
);

CREATE INDEX openai_upload_parts_upload ON openai_upload_parts(upload_id, created_at, id);
