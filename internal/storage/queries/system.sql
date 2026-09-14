-- name: GetGatewayMetadata :one
SELECT value FROM gateway_metadata WHERE key = ? LIMIT 1;

-- name: SetGatewayMetadata :exec
INSERT INTO gateway_metadata (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;
