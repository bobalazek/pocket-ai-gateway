-- name: GetProjectionMetadata :one
SELECT value FROM projection_metadata WHERE key = ? LIMIT 1;

-- name: SetProjectionMetadata :exec
INSERT INTO projection_metadata (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;
