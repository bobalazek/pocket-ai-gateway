CREATE TABLE media_jobs (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
    request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
    model_id TEXT NOT NULL REFERENCES public_models(id) ON DELETE RESTRICT,
    connection_id TEXT NOT NULL REFERENCES provider_connections(id) ON DELETE RESTRICT,
    provider TEXT NOT NULL,
    provider_job_id TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL CHECK (media_type IN ('image', 'video', 'audio', 'other')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'submitting', 'starting', 'processing', 'canceling', 'succeeded', 'failed', 'canceled', 'interrupted_unknown')),
    input_ciphertext BLOB,
    input_nonce BLOB,
    input_bytes INTEGER NOT NULL CHECK (input_bytes BETWEEN 2 AND 1048576),
    output_ciphertext BLOB,
    output_nonce BLOB,
    output_bytes INTEGER NOT NULL DEFAULT 0 CHECK (output_bytes BETWEEN 0 AND 4194304),
    error_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(error_json)),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    poll_failures INTEGER NOT NULL DEFAULT 0 CHECK (poll_failures BETWEEN 0 AND 8),
    next_poll_at INTEGER NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER,
    CHECK ((input_ciphertext IS NOT NULL AND length(input_ciphertext) = input_bytes + 16 AND length(input_nonce) = 12) OR (input_ciphertext IS NULL AND input_nonce IS NULL)),
    CHECK ((output_ciphertext IS NULL AND output_nonce IS NULL AND output_bytes = 0) OR (length(output_ciphertext) = output_bytes + 16 AND length(output_nonce) = 12))
);

CREATE UNIQUE INDEX media_jobs_provider_id ON media_jobs(connection_id, provider_job_id) WHERE provider_job_id <> '';
CREATE INDEX media_jobs_key_created ON media_jobs(key_id, created_at DESC, id DESC);
CREATE INDEX media_jobs_worker ON media_jobs(state, next_poll_at, created_at);
