#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
arch=$(docker version --format '{{.Server.Arch}}')
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported Docker server architecture: $arch" >&2; exit 1 ;;
esac
binary="dist/cross/pocket-ai-gateway-linux-$arch"
if [[ ! -f "$root/$binary" ]]; then
  echo "Build the dashboard and Linux artifacts with ./scripts/build.sh and ./scripts/cross-build.sh first" >&2
  exit 1
fi
module_cache=$(go env GOMODCACHE)

# Only the native Linux release executable is measured. The development
# toolchain supplies the separate load generator and ps; it is not deployed.
# All runtime data stays in the disposable container, without external network.
docker run --rm --network none --platform "linux/$arch" --user 65532:65532 \
  --mount "type=bind,source=$root,target=/src,readonly" \
  --mount "type=bind,source=$module_cache,target=/go/pkg/mod,readonly" \
  --workdir /src --env CGO_ENABLED=0 --env GOTOOLCHAIN=local --env GOCACHE=/tmp/go-build-cache \
  --env GIT_CONFIG_COUNT=1 --env GIT_CONFIG_KEY_0=safe.directory --env GIT_CONFIG_VALUE_0=/src \
  golang:1.27.1-bookworm \
  go run ./scripts/benchmark -binary "/src/$binary" "$@"
