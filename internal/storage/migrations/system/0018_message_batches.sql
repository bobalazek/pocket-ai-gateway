CREATE TABLE message_batches (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    processing_status TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (processing_status IN ('in_progress', 'canceling', 'ended')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    created_at INTEGER NOT NULL,
    cancel_initiated_at INTEGER,
    ended_at INTEGER,
    -- Resource and result retention deadline. Workers derive the 24-hour processing deadline from created_at.
    expires_at INTEGER NOT NULL,
    CHECK (expires_at > created_at),
    CHECK ((cancel_requested = 1 AND cancel_initiated_at IS NOT NULL) OR (cancel_requested = 0 AND cancel_initiated_at IS NULL)),
    CHECK (cancel_initiated_at IS NULL OR cancel_initiated_at >= created_at),
    CHECK ((processing_status = 'ended' AND ended_at IS NOT NULL) OR (processing_status <> 'ended' AND ended_at IS NULL)),
    CHECK (ended_at IS NULL OR ended_at >= created_at)
);

CREATE INDEX message_batches_key ON message_batches(key_id, created_at DESC, id DESC);
CREATE INDEX message_batches_expiry ON message_batches(expires_at);
CREATE INDEX message_batches_status ON message_batches(processing_status, created_at, id);

CREATE TABLE message_batch_items (
    batch_id TEXT NOT NULL REFERENCES message_batches(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 4),
    custom_id TEXT NOT NULL CHECK (length(custom_id) BETWEEN 1 AND 64 AND custom_id NOT GLOB '*[^A-Za-z0-9_-]*'),
    params_json BLOB NOT NULL CHECK (length(params_json) BETWEEN 1 AND 16777216),
    result_json BLOB CHECK (result_json IS NULL OR length(result_json) BETWEEN 1 AND 16777216),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'claimed', 'dispatching', 'settling', 'succeeded', 'errored', 'canceled', 'expired', 'interrupted_unknown')),
    request_id TEXT REFERENCES requests(id) ON DELETE SET NULL,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE SET NULL,
    reserved_result_bytes INTEGER NOT NULL CHECK (reserved_result_bytes BETWEEN 0 AND 16777216),
    lease_epoch TEXT NOT NULL DEFAULT '',
    claimed_at INTEGER,
    dispatch_started_at INTEGER,
    finished_at INTEGER,
    PRIMARY KEY (batch_id, ordinal),
    UNIQUE (batch_id, custom_id),
    CHECK ((state IN ('queued', 'claimed', 'dispatching') AND result_json IS NULL) OR (state IN ('settling', 'succeeded', 'errored', 'canceled', 'expired', 'interrupted_unknown') AND result_json IS NOT NULL)),
    CHECK ((state IN ('succeeded', 'errored', 'canceled', 'expired', 'interrupted_unknown') AND finished_at IS NOT NULL) OR (state NOT IN ('succeeded', 'errored', 'canceled', 'expired', 'interrupted_unknown') AND finished_at IS NULL)),
    CHECK (claimed_at IS NULL OR claimed_at >= 0),
    CHECK (dispatch_started_at IS NULL OR dispatch_started_at >= 0),
    CHECK (finished_at IS NULL OR finished_at >= 0)
);

CREATE INDEX message_batch_items_queue ON message_batch_items(state, batch_id, ordinal);
CREATE INDEX message_batch_items_request ON message_batch_items(request_id, state);
CREATE INDEX message_batch_items_attempt ON message_batch_items(attempt_id);
