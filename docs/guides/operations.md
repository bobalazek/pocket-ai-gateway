# Security, deployment, and recovery

Implementation requirements for SEC-01, OPS-01, and OPS-02. The `serve`, `version`, `snapshot`, and `restore` commands are implemented in Phase 1; other commands remain proposed interfaces.

## Data directory and deployment

~~~text
pocket_gateway_data/
  system.db           system metadata, owner identity, and sessions now; configuration/accounting later
  system.db-wal       SQLite-managed when present
  system.db-shm       SQLite-managed when present
  data.db             projection metadata now; history/events later
  data.db-wal         SQLite-managed when present
  data.db-shm         SQLite-managed when present
  instance.lock       OS-backed lock, not PID existence
  setup-code          owner-only bootstrap credential; temporary
~~~

Phase 2 adds protected key material. Phase 7 adds managed backup and temporary staging directories. Pre-migration snapshots are stored under `.pre-migration/` when a schema upgrade is pending.

Separate mutable data from the executable and application source. Use an absolute data path in services/containers; changing the working directory must not accidentally create an unrelated instance. Restrict the directory to the service user. Phase 1 verifies owner-only modes on macOS and Linux; Windows distribution waits for Phase 7 ACL and native smoke tests.

~~~sh
pocket-ai-gateway serve --data-dir /srv/pocket-gateway/data
pocket-ai-gateway version
pocket-ai-gateway doctor --data-dir /srv/pocket-gateway/data
~~~

Default listener is loopback. Remote deployment uses explicit binding with either configured TLS certificates in the Go server or a documented HTTPS reverse proxy. Trust proxy headers only from configured proxy addresses; validate forwarded origin/host consistently. No mandatory external proxy for loopback use.

Docker runs the same binary as a non-root user, with /data on a persistent local volume and a read-only application filesystem. Include CA certificates for cloud providers. Expose a minimal health check and honor termination/drain time. Local Ollama networking must be explained separately for host and container deployments.

One running writer per data directory. Do not mount SQLite WAL storage on NFS/shared multi-host volumes. Container replicas must not share this volume. A process manager may restart the single instance; it must not run overlapping writers.

## Identity and secret handling

Bootstrap code: high entropy, short-lived, retryable only with the same claimed owner identity and password during its recovery window, transmitted in a POST body, never a query string. A retry replaces the earlier setup session rather than accumulating sessions. Store the code in the owner-only `setup-code` file named at startup; never write its value to process logs or diagnostic bundles. First owner creation is transactional.

On an unclaimed instance, `serve` prints the setup URL and protected code-file path. Opening the dashboard redirects to that setup page. Restarting an unclaimed instance rotates the code; a claimed instance keeps the file only through the short recovery window, then removes it on startup.

Use Argon2id for human passwords with versioned parameters and calibrated cost, bounded verification concurrency, and login throttling. Use random session/API key secrets with cryptographic verifiers; do not apply a costly password KDF to every inference request. Password hashing parameters follow current reviewed guidance. [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)

Cookies are HttpOnly, SameSite, Secure under HTTPS, narrowly scoped, and server-revocable. Rotate session IDs on authentication, enforce idle/absolute expiry, require recent authentication for owner transfer, key-material/backup downloads, and destructive recovery actions. CSRF/origin validation applies to all cookie-authorized mutations.

Provider secrets use an authenticated encryption envelope with key version and random nonce; bind ciphertext to connection/secret identity as authenticated data. Store master.key outside SQLite by default, or accept an external protected secret file. No shell evaluation of secret references.

A key next to the DB protects against a DB-only leak; it does not protect a compromised host or full data-directory copy. Missing keys cause a visible recovery error, never automatic replacement. Back up the correct key version. Rotation re-encrypts transactionally and keeps recovery material until a verified new backup exists.

Logs/read APIs/UI bundles never contain full provider credentials, application key secrets, passwords, activation codes, or passphrases. Redact before persistence, not only in the dashboard. Errors from providers and failed imports are untrusted secret-bearing content.

Members see their own request metadata. Content capture requires explicit policy, is off by default, and includes tool names/arguments/results as sensitive content. Capture access is separate from ordinary operational metadata access and is audited. Default administrator access is metadata; the owner explicitly grants broader content access where required.

## Egress and inbound boundaries

Provider destinations are privileged configuration. Normalize schemes/ports/paths, reject URL userinfo/fragments, and allow HTTPS by default; local HTTP requires an explicit per-connection local-network policy.

Resolve and validate destination addresses at dial time to prevent validation/dial races. Block metadata/link-local/special destinations, including IPv4-mapped IPv6 bypasses. Private-network allowlists are explicit and narrow. Disable automatic redirects initially; any later redirect support repeats destination validation and strips credentials across origins.

Connection custom headers cannot override Host, transport semantics, gateway authorization, or hop-by-hop headers. Approved upstream credentials are handled by the adapter. Do not inherit ambient outbound proxy settings silently; configured proxies are part of the reviewed egress boundary.

The gateway does not download client-provided image/media URLs. Allowed URL representations may pass to an upstream provider only in supported fields. This does not promise the upstream provider never fetches them.

Destination allowlists and repeated address validation follow established SSRF defenses. [OWASP SSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)

Bound headers, request bodies, image/base64 payloads, embedding batch sizes, SSE event size, response error body size, timeouts, and concurrent requests. Restrict management origins/hosts, use CSP and local assets, and disable inference CORS unless explicitly configured. API keys in public frontend code remain exposed credentials.

Unauthenticated liveness reports only process availability; readiness reports minimal ready/unready state. Detailed DB, connection, policy, metrics, and version diagnostics are authenticated. Avoid high-cardinality user/key/prompt labels in exported metrics.

## Configuration import/export

~~~sh
pocket-ai-gateway config export --output config.json
pocket-ai-gateway config import --file config.json --dry-run
~~~

Online management CLI operations authenticate with an explicit scoped management token; offline recovery commands require exclusive local access.

Exports include schema version, providers without secret values, model/capability/price configuration, routes, and policy definitions. References to external secrets remain references. Exclude passwords, sessions, key verifiers, secret plaintext, captures, and accounting state from configuration export.

Import preview validates size/schema/version, names/IDs, references, permissions, policy widening, secret requirements, and embedding contract changes. Apply all-or-nothing with an audit/revision. Import cannot reset existing usage, overwrite a newer revision, or activate an unknown/free-price route silently.

Backups are the complete recovery mechanism. Config export is for reproducible setup and review.

## Backup

Phase 1 provides a protected, paired offline snapshot directory:

~~~sh
pocket-ai-gateway snapshot --data-dir /srv/pocket-gateway/data --output /secure/gateway-snapshot
pocket-ai-gateway restore --snapshot /secure/gateway-snapshot --data-dir /srv/pocket-gateway/restored-data
~~~

Stop the server before taking the snapshot. The command also enforces exclusive access. Restore validates both file hashes, database integrity, and supported migrations, then publishes into an absent destination directory. It never overwrites the active data directory. This snapshot is not encrypted and is not the portable Phase 7 backup format.

~~~sh
pocket-ai-gateway backup --data-dir /srv/pocket-gateway/data --output /secure/gateway-backup.pgb
pocket-ai-gateway restore --data-dir /srv/pocket-gateway/data --input /secure/gateway-backup.pgb --dry-run
~~~

Phase 1 introduces protected paired offline snapshots. Phase 7 adds the complete encrypted archive and scheduled local/S3-compatible backups. Use a maintained encryption format/library; select and review it before implementation rather than inventing a container cipher.

Portable backup contents: consistent snapshots/exports of both stores, outbox watermark and shared generation, required key material/key version, per-store schema/app version, manifest with sizes/checksums, and external-secret prerequisites. Resolve external references only where explicitly configured; otherwise show them as restore requirements.

Passphrases are entered interactively or read from a protected file, never required as command-line arguments. Scheduled backup uses an owner-configured protected encryption recipient/key source; no plaintext passphrase in settings or logs.

Take local snapshots through a tested SQLite snapshot primitive, not a copy of a live DB file. Use a certified consistent export for each remote store. SQLite provides backup and VACUUM INTO mechanisms. [SQLite backup documentation](https://www.sqlite.org/backup.html)

Serialize backup against key rotation/migration; briefly quiesce mutations and record/drain event delivery to capture matching system/data generations. Write to private staging, validate/checksum, sync, and atomically publish a completed artifact. Never mark a partial artifact successful.

Manual/scheduled jobs have running/succeeded/failed states, bounded execution/storage, and visible last-success/last-failure timestamps. No overlapping jobs. Default scheduled policy, when enabled: daily, retain seven successful archives subject to a visible byte limit; never delete the last valid backup before a new one succeeds.

Backups contain credentials and possibly captured user content. Download is owner-only with recent authentication. Off-host S3-compatible backup is part of v0.1; local-only backups remain available without remote credentials.

### S3-compatible destination

Configure endpoint, region, bucket/prefix, addressing style where required, schedule/timezone, retention, and a protected credential reference. Use a maintained S3 client, TLS, least-privilege object access, and optional provider-side encryption in addition to archive encryption. Never write keys into exported configuration.

Upload only verified encrypted archives with unique generation-based object names. Verify stored checksum/size, handle multipart completion/abort, use bounded retries/backoff, and publish the completed manifest last. Failed uploads do not delete the last good local/remote archive. Retention only deletes completed artifacts after a newer verified backup exists.

Use UTC nightly scheduling by default once the operator enables a destination; allow an explicit timezone and test DST/skipped-run behavior. No overlapping jobs or infinite catch-up queue. Show local creation versus remote upload success separately.

Restore can download a selected S3 generation into protected staging, then perform the same archive/DB checks as a local restore. Test expired credentials, denied access, wrong region/endpoint, partial upload, eventual retry, corrupted object, quota/full disk, and a clean-machine restore. S3-compatible providers must pass the exercised operation tests; similar branding is insufficient. [Amazon S3 documentation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html)

## Restore

Restore is an offline CLI operation under the instance lock. Dashboard offers backup creation/download and restore guidance; it does not replace a live database.

1. Validate archive format/authentication, bounded decompressed size, checksum, supported schema, path allowlist, and all required files. Reject symlinks, traversal, duplicate/conflicting entries, and archive bombs.
2. Extract into protected staging. Verify both stores, matching generation/watermark, migration compatibility, local foreign keys or remote-equivalent constraints, and credential decryption.
3. Preserve the current installation in a recoverable pre-restore snapshot. Check free disk space before changing active data.
4. Promote local stores through a crash-recoverable rename/journal sequence. Remote restores provision/import into separately staged store targets and switch the authoritative configuration only after validation; never overwrite a running remote primary piecemeal. Startup detects an interrupted promotion and selects a complete recoverable generation.
5. Invalidate browser sessions, activation/recovery tokens, and management tokens. Require local owner recovery/re-authentication as documented.
6. Start in recovery mode with inference admission disabled. Show the backup timestamp and potentially missing usage/revocations since it was taken. Classify active attempts as unknown, review/rotate affected application keys, reconcile or conservatively reserve the gap, then let the owner explicitly re-enable inference.

An older snapshot can resurrect revoked keys and omit later spend. Recovery mode is mandatory; restore is not a way to reset limits unnoticed. Missing external secrets leave those connections disabled.

Verify both restoration into an empty directory and restoration over an existing instance. A wrong passphrase, corrupt DB, failed migration, disk-full condition, or interrupted swap must leave the original data usable or a clearly identified recoverable snapshot.

## Upgrade and rollback

v0.1 upgrades:

1. Read release/schema compatibility notes; obtain the correct platform artifact and verify its signature/provenance/checksum.
2. Drain and stop the server; create and verify a protected backup.
3. Replace the executable, preserving the absolute data directory. Docker users replace the pinned image tag/digest.
4. Start the new version; validate and snapshot before migrations; verify health, login, policy state, and a configured test request.
5. On failure, stop and restore the matching previous binary and backup through recovery mode.

An older binary refuses a newer schema. No automatic destructive down-migrations. A successfully migrated DB cannot be assumed safe with the old executable.

Phase 8 may add an explicit update command with configured release source, authenticated manifests, platform validation, staged replacement, restart/health checks, and rollback. Handle Windows executable locks and service ownership explicitly. Automatic background installation is excluded.

## Release checks and operational limits

Keep admission/accounting writes durable, monitor busy/disk-full/WAL growth, and expose unknown-usage reconciliation. Maintenance jobs are bounded and local by default. User-requested scheduling inside the product is not a hosted dependency.

CI should cover formatting, vet, focused unit/integration/race tests, parser fuzz seeds, vulnerability checks, UI type/build/browser checks, and supported-platform smoke tests. Go documents vet, the race detector, fuzzing, and govulncheck as complementary tools. [Go security practices](https://go.dev/doc/security/best-practices)

Before release verify the MIT LICENSE, dependency notices, CONTRIBUTING, SECURITY with a real reporting channel, CHANGELOG, build instructions, adapter guide, compatibility matrix, troubleshooting, backup/restore/upgrade guide, checksums/signatures/provenance, and SBOM. Do not invent a maintainer email or claim a security audit occurred.
