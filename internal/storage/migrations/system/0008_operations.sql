CREATE TABLE operation_settings (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    backup_enabled INTEGER NOT NULL DEFAULT 0 CHECK (backup_enabled IN (0, 1)),
    backup_interval_hours INTEGER NOT NULL DEFAULT 24 CHECK (backup_interval_hours BETWEEN 1 AND 720),
    backup_retention_count INTEGER NOT NULL DEFAULT 14 CHECK (backup_retention_count BETWEEN 1 AND 365),
    backup_destination TEXT NOT NULL DEFAULT 'local' CHECK (backup_destination IN ('local', 's3')),
    local_directory TEXT NOT NULL DEFAULT '',
    s3_endpoint TEXT NOT NULL DEFAULT '',
    s3_region TEXT NOT NULL DEFAULT 'us-east-1',
    s3_bucket TEXT NOT NULL DEFAULT '',
    s3_prefix TEXT NOT NULL DEFAULT 'pocket-ai-gateway',
    s3_access_key_env TEXT NOT NULL DEFAULT 'AWS_ACCESS_KEY_ID',
    s3_secret_key_env TEXT NOT NULL DEFAULT 'AWS_SECRET_ACCESS_KEY',
    request_retention_days INTEGER NOT NULL DEFAULT 90 CHECK (request_retention_days BETWEEN 1 AND 3650),
    audit_retention_days INTEGER NOT NULL DEFAULT 365 CHECK (audit_retention_days BETWEEN 30 AND 3650),
    revision INTEGER NOT NULL DEFAULT 1,
    updated_at INTEGER NOT NULL
);

INSERT INTO operation_settings (singleton, updated_at) VALUES (1, unixepoch('subsec') * 1000);

CREATE TABLE backup_jobs (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed')),
    destination TEXT NOT NULL CHECK (destination IN ('local', 's3')),
    archive_name TEXT NOT NULL,
    checksum TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    snapshot_generation TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    finished_at INTEGER
);

CREATE INDEX backup_jobs_started ON backup_jobs(started_at DESC, id DESC);
