# Deployment

Pocket AI Gateway runs as one process and one writer per data directory. The dashboard, database migrations, and static assets are inside the executable.

## Build once, deploy one executable

Build on the target operating system and architecture:

```sh
git clone https://github.com/bobalazek/pocket-ai-gateway.git
cd pocket-ai-gateway
./scripts/build.sh
./scripts/verify.sh
```

`dist/pocket-ai-gateway` contains the server, dashboard, and migrations. Copy it to the server; Node.js, pnpm, the repository, and the dashboard source are not needed after the build. Build separately for each target platform, or use `./scripts/package.sh VERSION` to create the supported platform archives. Do not copy a macOS binary to Linux or an Arm binary to an amd64 host.

Create a private, persistent data directory owned by the service account:

```sh
sudo useradd --system --user-group --home-dir /var/lib/pocket-ai-gateway --shell /usr/sbin/nologin pocket-gateway
sudo install -d -m 0700 -o pocket-gateway -g pocket-gateway \
  /var/lib/pocket-ai-gateway /var/lib/pocket-ai-gateway_backups
sudo install -m 0755 dist/pocket-ai-gateway /usr/local/bin/pocket-ai-gateway
```

Run locally behind an HTTPS reverse proxy:

```sh
sudo -u pocket-gateway /usr/local/bin/pocket-ai-gateway serve \
  --listen 127.0.0.1:8080 \
  --data-dir /var/lib/pocket-ai-gateway \
  --public-url https://gateway.example.com
```

`--public-url` pins browser Host and Origin checks and controls secure cookies. The gateway does not trust forwarded headers. Configure the proxy to preserve the original `Host` header.

On a fresh data directory, the first valid setup request becomes the owner. Keep internet ingress restricted until you have completed owner setup; an unclaimed public instance can be claimed by someone else.

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
TimeoutStopSec=20s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/pocket-ai-gateway /var/lib/pocket-ai-gateway_backups

[Install]
WantedBy=multi-user.target
```

Enable it and read the first-run setup path from the service log:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now pocket-ai-gateway
sudo journalctl -u pocket-ai-gateway
```

## Docker

The gateway is one container. `compose.yaml` only saves typing: it builds the image and applies the volumes, loopback port, read-only root filesystem, and restart policy below. Use whichever you prefer.

Without Compose:

```sh
docker build --build-arg VERSION=local -t pocket-ai-gateway:local .
docker run -d --name pocket-ai-gateway --restart unless-stopped \
  --read-only --tmpfs /tmp --init --stop-timeout 20 \
  -p 127.0.0.1:8080:8080 \
  -v pocket-gateway-data:/data \
  -v pocket-gateway-backups:/data_backups -v pocket-gateway-backups:/backups \
  -e POCKET_AI_GATEWAY_BACKUP_KEY \
  pocket-ai-gateway:local
docker logs pocket-ai-gateway
```

After a tagged release, replace the build with `ghcr.io/bobalazek/pocket-ai-gateway:<version>`. Upgrade by pulling the new tag and recreating the container with the same volumes.

With Compose, the same local deployment at `http://localhost:8080`:

```sh
docker compose up --build -d
docker compose logs gateway
```

It stores `/data` and the default `/data_backups` directory in separate named volumes. The backup volume is also mounted at the former `/backups` path so existing saved settings keep working during upgrade. Set `POCKET_AI_GATEWAY_BACKUP_KEY` in the Compose environment to enable manual encrypted backups; enable and schedule recurring backups in **Settings**.

For production, run the container behind HTTPS and set the public origin:

```yaml
services:
  gateway:
    environment:
      POCKET_AI_GATEWAY_PUBLIC_URL: https://gateway.example.com
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

GitHub Container registry creates a new package as private by default, even when the source repository is public. After the first release image is pushed, make the package public in GitHub package settings and test a pull without registry credentials before advertising the image. Package visibility is separate from repository visibility. See [GitHub's package visibility guide](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).

Publishing requires an explicit `--push`; building locally does not publish anything.

## Configuration

| Setting | Flag | Environment variable | Default |
| --- | --- | --- | --- |
| Listener | `--listen` | `POCKET_AI_GATEWAY_LISTEN` | `127.0.0.1:8080` |
| Data directory | `--data-dir` | `POCKET_AI_GATEWAY_DATA_DIR` | `./pocket_gateway_data` |
| Browser origin | `--public-url` | `POCKET_AI_GATEWAY_PUBLIC_URL` | listener origin |
| Backup encryption key | — | `POCKET_AI_GATEWAY_BACKUP_KEY` | unset; backups unavailable |

Binding beyond loopback requires an HTTPS public URL. `--allow-insecure-http` exists only for an explicit `localhost` or loopback public URL; the container image passes it by default so the loopback-published Compose setup works. Self-update settings are covered in [Optional standalone self-update](#optional-standalone-self-update).

## Back up and restore

Put the [backup key](operations.md#encrypted-backups) in `/etc/pocket-ai-gateway.env` as `POCKET_AI_GATEWAY_BACKUP_KEY=...`, restrict the file to root (mode `0600`), and preserve a separate recovery copy. Stop the service, then create an encrypted backup using that same environment file:

```sh
sudo systemctl stop pocket-ai-gateway
sudo systemd-run --pipe --collect --uid=pocket-gateway \
  --property=EnvironmentFile=/etc/pocket-ai-gateway.env \
  /usr/local/bin/pocket-ai-gateway backup \
  --data-dir /var/lib/pocket-ai-gateway \
  --output /var/lib/pocket-ai-gateway_backups/manual.pagbak
sudo systemctl start pocket-ai-gateway
```

The encrypted archive contains both databases and the credential master key. Restore uses the same key, authenticates the archive, validates hashes and database integrity, and only writes to an absent destination:

```sh
sudo install -d -m 0700 -o pocket-gateway -g pocket-gateway /var/lib/pocket-ai-gateway-recovery
sudo systemd-run --pipe --collect --uid=pocket-gateway \
  --property=EnvironmentFile=/etc/pocket-ai-gateway.env \
  /usr/local/bin/pocket-ai-gateway restore-backup \
  --archive /var/lib/pocket-ai-gateway_backups/manual.pagbak \
  --data-dir /var/lib/pocket-ai-gateway-recovery/restored
```

Scheduled local and S3-compatible backups are configured in the dashboard. See [operations and recovery](operations.md) for key handling and the complete recovery procedure.

## Upgrade

1. Build and verify the new executable or image.
2. Stop the gateway and create a verified encrypted backup.
3. Replace the executable or pinned image while retaining the data directory.
4. Start the gateway and verify `GET /readyz`, dashboard login, and a configured test request.
5. If the upgrade fails, stop it and restore the matching previous executable and backup into a clean directory.

The gateway refuses unsupported newer database schemas. Do not run two versions against the same data directory.

### Optional standalone self-update

Self-update is for release binaries installed in a directory writable by the service account. Root-owned `/usr/local/bin` installations keep using the manual procedure above. Containers must replace the pinned image instead of mutating a container filesystem.

Release maintainers generate a 32-byte Ed25519 seed outside the repository, store it as the `POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY` GitHub Actions secret, and distribute only the derived public key:

```sh
export POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY="$(openssl rand -base64 32)"
go run ./scripts/release-manifest --print-public-key
```

Store the seed in a maintainer-controlled password manager and set the GitHub Actions secret from standard input (`printf '%s' "$POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY" | gh secret set POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY`). Never commit or publish the seed. The release job refuses to publish when the secret is missing, verifies the detached signature and both Linux binaries after signing, and checks the final `SHA256SUMS`. Anyone with the independently distributed public key can verify downloaded release files locally:

```sh
go run ./scripts/release-manifest --verify --version vX.Y.Z \
  --directory dist/release --public-key 'base64-public-key-from-the-release-maintainer'
(cd dist/release && sha256sum --check SHA256SUMS)
```

On the server, stop the service and configure that base64 public key. The first command is a dry-run: it downloads and verifies the signed manifest and exact Linux artifact, stages it beside the executable, and runs its embedded version check without changing the installation.

```sh
sudo systemctl stop pocket-ai-gateway
export POCKET_AI_GATEWAY_UPDATE_PUBLIC_KEY='base64-public-key-from-the-release-maintainer'

/opt/pocket-ai-gateway/pocket-ai-gateway update \
  --data-dir /var/lib/pocket-ai-gateway

/opt/pocket-ai-gateway/pocket-ai-gateway update \
  --data-dir /var/lib/pocket-ai-gateway \
  --apply

sudo systemctl start pocket-ai-gateway
curl --fail --header 'Host: gateway.example.com' http://127.0.0.1:8080/readyz
```

`--apply` refuses a running data directory, writes a paired pre-update snapshot beside the data directory, atomically exchanges the Linux executable, starts the new binary temporarily on loopback, requires `/readyz`, and stops it. Failure restores both the previous executable and snapshot. Success reports the retained snapshot and uniquely named `.previous-*` binary paths. Use `--manifest-url` and `--signature-url` only for a separately trusted release channel. `--allow-downgrade` still requires a valid signature and exists for deliberate rollback.

For a manual loopback readiness request, match the `Host` header to your configured public origin as above. The updater supplies a separate loopback origin only to its temporary probe; it does not change the deployed origin.

Maintainers can rehearse this flow locally with `./scripts/update-e2e.sh` after building the dashboard. The test uses real Linux executables, disposable signing keys/stores, and a container without external network access; it never updates an installed gateway.
