# Deployment

Pocket AI Gateway runs as one process and one writer per data directory. The dashboard, database migrations, and static assets are inside the executable.

## Build a standalone executable

Build on the target operating system and architecture:

```sh
git clone https://github.com/bobalazek/pocket-ai-gateway.git
cd pocket-ai-gateway
./scripts/build.sh
./scripts/verify.sh
```

Copy `dist/pocket-ai-gateway` to the server. Node.js, pnpm, the repository, and the dashboard source are not needed after the build. Build separately for each target platform; do not copy a macOS binary to Linux or an Arm binary to an amd64 host.

Create a private, persistent data directory owned by the service account:

```sh
sudo install -d -m 0700 -o pocket-gateway -g pocket-gateway /var/lib/pocket-ai-gateway
sudo install -m 0755 dist/pocket-ai-gateway /usr/local/bin/pocket-ai-gateway
```

Run locally behind an HTTPS reverse proxy:

```sh
/usr/local/bin/pocket-ai-gateway serve \
  --listen 127.0.0.1:8080 \
  --data-dir /var/lib/pocket-ai-gateway \
  --public-url https://gateway.example.com
```

`--public-url` pins browser Host and Origin checks and controls secure cookies. The gateway does not trust forwarded headers. Configure the proxy to preserve the original `Host` header.

Example Caddy configuration:

```caddyfile
gateway.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

Do not expose the loopback listener directly. Terminate TLS at the proxy and restrict access as appropriate for the installation.

## systemd

Create `/etc/systemd/system/pocket-ai-gateway.service`:

```ini
[Unit]
Description=Pocket AI Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pocket-gateway
Group=pocket-gateway
ExecStart=/usr/local/bin/pocket-ai-gateway serve --listen 127.0.0.1:8080 --data-dir /var/lib/pocket-ai-gateway --public-url https://gateway.example.com
EnvironmentFile=-/etc/pocket-ai-gateway.env
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/pocket-ai-gateway /var/backups/pocket-ai-gateway

[Install]
WantedBy=multi-user.target
```

Enable it and read the first-run setup path from the service log:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now pocket-ai-gateway
sudo journalctl -u pocket-ai-gateway
```

## Docker Compose

The included `compose.yaml` is a local deployment at `http://localhost:8080`:

```sh
docker compose up --build -d
docker compose logs gateway
```

It stores `/data` and `/backups` in separate named volumes. Set `POCKET_AI_GATEWAY_BACKUP_KEY` in the Compose environment, then choose `/backups` as the local backup directory in **Settings**.

For production, run the container behind HTTPS and replace the local command with the public origin:

```yaml
services:
  gateway:
    command:
      - serve
      - --listen
      - 0.0.0.0:8080
      - --data-dir
      - /data
      - --public-url
      - https://gateway.example.com
```

Publish the container port only to the reverse-proxy host or private container network. Never share one SQLite volume between replicas, hosts, or overlapping gateway processes.

To build a versioned multi-platform image with Docker Buildx:

```sh
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=0.1.0 \
  --tag ghcr.io/your-org/pocket-ai-gateway:0.1.0 \
  .
```

Publishing requires an explicit `--push`; building locally does not publish anything.

## Configuration

| Setting | Flag | Environment variable | Default |
| --- | --- | --- | --- |
| Listener | `--listen` | `POCKET_AI_GATEWAY_LISTEN` | `127.0.0.1:8080` |
| Data directory | `--data-dir` | `POCKET_AI_GATEWAY_DATA_DIR` | `./pocket_gateway_data` |
| Browser origin | `--public-url` | `POCKET_AI_GATEWAY_PUBLIC_URL` | listener origin |
| Backup encryption key | — | `POCKET_AI_GATEWAY_BACKUP_KEY` | unset; backups unavailable |

Binding beyond loopback requires an HTTPS public URL. `--allow-insecure-http` exists only for an explicit `localhost` or loopback public URL, as used by the local Compose file.

## Back up and restore

Set the external archive key in the service environment, preserve a separate recovery copy, then stop the process and create an encrypted backup:

```sh
sudo systemctl stop pocket-ai-gateway
sudo -u pocket-gateway pocket-ai-gateway backup \
  --data-dir /var/lib/pocket-ai-gateway \
  --output /secure/pocket-ai-gateway.pagbak
sudo systemctl start pocket-ai-gateway
```

The encrypted archive contains both databases and the credential master key. Restore authenticates the archive, validates hashes and database integrity, and only writes to an absent destination:

```sh
pocket-ai-gateway restore-backup \
  --archive /secure/pocket-ai-gateway.pagbak \
  --data-dir /var/lib/pocket-ai-gateway-restored
```

Scheduled local and S3-compatible backups are configured in the dashboard. See [operations and recovery](operations.md) for key handling and the complete recovery procedure.

## Upgrade

1. Build and verify the new executable or image.
2. Stop the gateway and create a verified encrypted backup.
3. Replace the executable or pinned image while retaining the data directory.
4. Start the gateway and verify `GET /readyz`, dashboard login, and a configured test request.
5. If the upgrade fails, stop it and restore the matching previous executable and backup into a clean directory.

The gateway refuses unsupported newer database schemas. Do not run two versions against the same data directory.
