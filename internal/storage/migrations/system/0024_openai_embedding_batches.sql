CREATE TABLE openai_batches_v24 (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    input_file_id TEXT NOT NULL CHECK (length(input_file_id) BETWEEN 1 AND 128),
    endpoint TEXT NOT NULL CHECK (endpoint IN ('/v1/responses', '/v1/chat/completions', '/v1/embeddings')),
    completion_window TEXT NOT NULL CHECK (completion_window = '24h'),
    model_id TEXT NOT NULL CHECK (length(model_id) BETWEEN 1 AND 200),
    status TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (status IN ('in_progress', 'finalizing', 'cancelling', 'completed', 'cancelled', 'expired')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    metadata_json BLOB NOT NULL CHECK (length(metadata_json) BETWEEN 2 AND 16384),
    output_expiry_seconds INTEGER NOT NULL CHECK (output_expiry_seconds BETWEEN 3600 AND 2592000),
    output_file_id TEXT REFERENCES openai_files(id) ON DELETE SET NULL,
    error_file_id TEXT REFERENCES openai_files(id) ON DELETE SET NULL,
    request_total INTEGER NOT NULL CHECK (request_total BETWEEN 1 AND 4),
    request_completed INTEGER NOT NULL DEFAULT 0 CHECK (request_completed BETWEEN 0 AND request_total),
    request_failed INTEGER NOT NULL DEFAULT 0 CHECK (request_failed BETWEEN 0 AND request_total),
    usage_known INTEGER NOT NULL DEFAULT 0 CHECK (usage_known IN (0, 1)),
    usage_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (usage_input_tokens >= 0),
    usage_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (usage_output_tokens >= 0),
    usage_cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK (usage_cached_tokens >= 0),
    usage_reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (usage_reasoning_tokens >= 0),
    created_at INTEGER NOT NULL,
    in_progress_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    cancelling_at INTEGER,
    terminal_at INTEGER,
    retention_expires_at INTEGER NOT NULL,
    CHECK (expires_at = created_at + 86400000),
    CHECK (retention_expires_at = created_at + 2592000000),
    CHECK ((cancel_requested = 1 AND cancelling_at IS NOT NULL) OR (cancel_requested = 0 AND cancelling_at IS NULL)),
    CHECK (request_completed + request_failed <= request_total),
    CHECK ((status IN ('completed', 'cancelled', 'expired') AND terminal_at IS NOT NULL) OR (status IN ('in_progress', 'finalizing', 'cancelling') AND terminal_at IS NULL)),
    CHECK (cancelling_at IS NULL OR cancelling_at >= created_at),
    CHECK (terminal_at IS NULL OR terminal_at >= created_at),
    CHECK (output_file_id IS NULL OR output_file_id <> error_file_id)
);

CREATE TABLE openai_batch_items_v24 (
    batch_id TEXT NOT NULL REFERENCES openai_batches_v24(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 4),
    custom_id TEXT NOT NULL CHECK (length(CAST(custom_id AS BLOB)) BETWEEN 1 AND 64),
    result_id TEXT NOT NULL UNIQUE CHECK (length(result_id) BETWEEN 16 AND 128),
    request_bytes INTEGER NOT NULL CHECK (request_bytes BETWEEN 2 AND 16777216),
    request_ciphertext BLOB NOT NULL CHECK (length(request_ciphertext) = request_bytes + 16),
    request_nonce BLOB NOT NULL CHECK (length(request_nonce) = 12),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'claimed', 'dispatching', 'settling', 'succeeded', 'failed', 'canceled', 'expired', 'interrupted_unknown')),
    result_bytes INTEGER,
    result_ciphertext BLOB,
    result_nonce BLOB,
    request_id TEXT REFERENCES requests(id) ON DELETE SET NULL,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE SET NULL,
    usage_known INTEGER NOT NULL DEFAULT 1 CHECK (usage_known IN (0, 1)),
    reserved_result_bytes INTEGER NOT NULL CHECK (reserved_result_bytes BETWEEN 1 AND 16777245),
    lease_epoch TEXT NOT NULL DEFAULT '',
    claimed_at INTEGER,
    dispatch_started_at INTEGER,
    finished_at INTEGER,
    PRIMARY KEY (batch_id, ordinal),
    UNIQUE (batch_id, custom_id),
    CHECK ((state IN ('queued', 'claimed', 'dispatching') AND result_bytes IS NULL AND result_ciphertext IS NULL AND result_nonce IS NULL) OR (state IN ('settling', 'succeeded', 'failed', 'canceled', 'expired', 'interrupted_unknown') AND result_bytes BETWEEN 1 AND 16777216 AND length(result_ciphertext) = result_bytes + 16 AND length(result_nonce) = 12)),
    CHECK ((state IN ('succeeded', 'failed', 'canceled', 'expired', 'interrupted_unknown') AND finished_at IS NOT NULL) OR (state NOT IN ('succeeded', 'failed', 'canceled', 'expired', 'interrupted_unknown') AND finished_at IS NULL)),
    CHECK (claimed_at IS NULL OR claimed_at >= 0),
    CHECK (dispatch_started_at IS NULL OR dispatch_started_at >= 0),
    CHECK (finished_at IS NULL OR finished_at >= 0)
);

INSERT INTO openai_batches_v24 SELECT * FROM openai_batches;
INSERT INTO openai_batch_items_v24 SELECT * FROM openai_batch_items;

DROP TABLE openai_batch_items;
DROP TABLE openai_batches;
ALTER TABLE openai_batches_v24 RENAME TO openai_batches;
ALTER TABLE openai_batch_items_v24 RENAME TO openai_batch_items;

CREATE INDEX openai_batches_key ON openai_batches(key_id, created_at DESC, id DESC);
CREATE INDEX openai_batches_queue ON openai_batches(status, created_at, id);
CREATE INDEX openai_batches_retention ON openai_batches(retention_expires_at);
CREATE INDEX openai_batch_items_queue ON openai_batch_items(state, batch_id, ordinal);
CREATE INDEX openai_batch_items_request ON openai_batch_items(request_id, state);
CREATE INDEX openai_batch_items_attempt ON openai_batch_items(attempt_id);
