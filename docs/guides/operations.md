# Operations and recovery

Pocket AI Gateway keeps runtime state in one private data directory. `system.db` contains identity, configuration, routing, limits, accounting, and the durable projection outbox. `data.db` contains query projections. `master.key` encrypts stored provider credentials. Back up the pair and key together.

## Health and shutdown

- `GET /healthz` is unauthenticated process liveness.
- `GET /readyz` checks both databases and projection capacity, then returns only ready or unavailable.
- Signed-in owners/admins can inspect each database and usage-projection check on **Status** or through `GET /api/v1/admin/status`; the response includes the current backlog and never exposes raw database errors. Runtime diagnostics remain available in **Settings** and `GET /api/v1/admin/diagnostics`. The diagnostics API includes a sampled `goroutines` count for investigating concurrency and resource growth; it exposes no stacks, heap dumps, or public profiling endpoint.
- `SIGINT` and `SIGTERM` stop admission through the HTTP server, allow active requests up to the 10-second shutdown deadline, cancel workers, and close both databases.

Terminate through the process manager before forcing a kill. A single process owns each data directory through `instance.lock`; never share a local SQLite directory between replicas or place it on NFS.

The authenticated Status page and `GET /api/v1/admin/status` also show local alerts when a database or usage projection is unavailable, the latest backup failed, or at least 20 completed requests in the past hour have a failure rate of 5% or more. A request recovered by fallback counts as a successful request but may still have a failed attempt. Usage summaries report both counts; error rate is failed requests divided by successful plus failed requests. Alerts are recomputed when Status is read; the gateway does not send email, webhooks, or public telemetry. Operators who need paging can poll the authenticated status endpoint from their own monitoring system.

## Encrypted backups

Set one external archive key before enabling backups. Generate it once, store it in a secret manager, and keep a separately protected recovery copy:

```sh
export POCKET_AI_GATEWAY_BACKUP_KEY="$(openssl rand -base64 32)"
```

The value must be standard base64 for exactly 32 random bytes. It is never stored in SQLite or included in an export. Losing it makes encrypted backups unrecoverable.

Create a backup while the service is stopped:

```sh
./pocket-ai-gateway backup \
  --data-dir /srv/pocket-ai-gateway/data \
  --output /srv/pocket-ai-gateway/backups/manual.pagbak
```

The command snapshots each live SQLite database with `VACUUM INTO`, records a shared generation, verifies database integrity, packages only the manifest, databases, and optional `master.key`, then encrypts authenticated 64 KiB records with AES-256-GCM. It publishes the completed archive atomically and prints its SHA-256 checksum.

The owner can configure scheduled local or S3-compatible backups in **Settings**. S3 uses HTTPS except for loopback testing, AWS Signature Version 4, bounded retries, and environment variable names for credentials. Archives are encrypted before upload. The first scheduled attempt happens when the service starts; later checks run every 15 minutes and honor the configured interval. Local retention never deletes the newest configured number of completed `.pagbak` files.

For S3 recovery, download the archive named in **Settings → Backups** from the configured bucket and prefix using your storage client's authenticated download. Compare its SHA-256 and byte size with the backup job, then use the offline restore commands below with the original backup encryption key. Configure object retention in the storage service; the gateway's retention count applies to local archives. `./scripts/compose-e2e.sh --s3` rehearses upload, download, and clean-volume restore against a disposable local S3 server; see [test requirements and limits](../project/testing.md).

Docker Compose keeps the default backup directory in its own named volume. Copy important archives off the Docker host or use the S3-compatible destination; a second volume on the same host is not a disaster-recovery copy. Run `./scripts/compose-e2e.sh` after deployment changes to rehearse onboarding, backup, clean-volume restore, readiness, login, and restart persistence with disposable volumes.

Export an archive from the Compose backup volume:

```sh
mkdir -p gateway-recovery
docker compose cp --archive gateway:/data_backups/<archive>.pagbak ./gateway-recovery/
```

Restore it with the same pinned image and backup key into a clean host directory owned by the image's nonroot UID/GID (`65532`):

```sh
sudo install -d -m 0700 -o 65532 -g 65532 gateway-restored
docker run --rm \
  --env POCKET_AI_GATEWAY_BACKUP_KEY \
  --volume "$PWD/gateway-recovery:/recovery:ro" \
  --volume "$PWD/gateway-restored:/restore" \
  pocket-ai-gateway:local \
  restore-backup --archive /recovery/<archive>.pagbak --data-dir /restore/data
```

After verifying the restored directory, switch `/data` with a Compose override:

```yaml
# compose.restored.yaml
services:
  gateway:
    volumes:
      - ./gateway-restored/data:/data
```

```sh
docker compose stop gateway
docker compose -f compose.yaml -f compose.restored.yaml up -d gateway
```

Keep the old named data volume until sign-in, providers, history, usage, and `/readyz` have been checked.

## Restore

Restore is offline and always targets an absent directory:

```sh
export POCKET_AI_GATEWAY_BACKUP_KEY='the-same-recovery-key'
./pocket-ai-gateway restore-backup \
  --archive /srv/pocket-ai-gateway/backups/manual.pagbak \
  --data-dir /srv/pocket-ai-gateway/restored
```

The restore authenticates every encrypted record, rejects truncation, trailing data, duplicate or unexpected archive entries, verifies checksums and SQLite integrity, rejects newer schemas, and stages the result before publishing it. Start the matching gateway binary against the restored directory and verify login, providers, request history, usage, and `/readyz` before switching traffic.

The older `snapshot` and `restore` commands remain available for protected, unencrypted local snapshot directories. They require exclusive access and are useful for migration testing, not off-host storage.

## Portable configuration

**Settings** exports a versioned JSON document containing provider definitions without credentials, upstream and public models, routes, limit policies, prices, catalog settings, and operations settings. It excludes users, sessions, API keys, provider credential values, accounting state, and request history.

Import is an owner-only, two-step preview and transactional apply. Existing matching IDs are updated, immutable price IDs are retained, unrelated resources remain, and any database constraint failure rolls back the entire import. Provider credentials stay local when the adapter and endpoint are unchanged and are cleared when either changes. Use encrypted backups for disaster recovery.

## Retention

Retention removes expired gateway-owned Responses and stored Chat Completions, old audit records, and projected event payloads. It preserves authoritative requests, attempts, ledgers, assessments, interrupted work, active reservations, quota periods, token-bucket state, pricing jobs, and daily projections, so cleanup cannot erase repricing facts or restore spend and rate-limit capacity. The owner runs it manually from **Settings** after changing the periods.

## Upgrades and rollback

1. Create and restore-test an encrypted backup.
2. Stop the service and preserve the current binary.
3. Replace the binary with the verified artifact for the same OS and architecture.
4. Start it against the existing data directory. Pending migrations create a protected pre-migration snapshot automatically.
5. Verify `/readyz`, sign-in, provider configuration, and one non-billable route preview.

If startup fails after a migration, stop the service and use the previous binary with a restored pre-upgrade backup in a new directory. Never run an older binary directly against a newer schema.

For a standalone Linux installation owned by the service account, the optional `update` command automates the same safety sequence. It defaults to dry-run, requires the configured Ed25519 release public key, and verifies the detached manifest signature, platform, artifact size, SHA-256, and embedded version. Applying requires the service to be stopped. It snapshots both databases and `master.key`, swaps the executable on the same filesystem, probes the new binary through `/readyz`, and rolls back binary and data after a failed probe. The successful result prints the uniquely named `.previous-*` binary and snapshot paths. Docker deployments continue to replace a pinned image.

## Disk and backpressure

Keep free space for both databases, WAL files, one temporary backup, and migration snapshots. SQLite write failures stop the affected request before provider dispatch where possible. A provider response accepted before a persistence failure remains conservatively reserved and is recovered as unknown. The durable outbox is bounded; readiness and authenticated diagnostics expose pending projection events.

Pocket AI Gateway sends no public telemetry. Logs contain runtime state and safe identifiers, never prompts, provider credentials, application key secrets, passwords, recovery codes, or backup keys.
