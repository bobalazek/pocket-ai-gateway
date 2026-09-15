ALTER TABLE stored_responses ADD COLUMN state TEXT NOT NULL DEFAULT 'completed'
    CHECK (state IN ('queued', 'running', 'completed', 'failed', 'cancelled', 'interrupted_unknown'));
ALTER TABLE stored_responses ADD COLUMN request_json BLOB;
ALTER TABLE stored_responses ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1));
ALTER TABLE stored_responses ADD COLUMN lease_epoch TEXT NOT NULL DEFAULT '';
ALTER TABLE stored_responses ADD COLUMN claimed_at INTEGER;
ALTER TABLE stored_responses ADD COLUMN finished_at INTEGER;

UPDATE stored_responses SET finished_at = created_at WHERE state = 'completed';

CREATE INDEX stored_responses_queue ON stored_responses(state, created_at, id);
