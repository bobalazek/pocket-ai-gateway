#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ ! -f "$root/web/out/index.html" ]]; then
  echo "Build the embedded dashboard first with ./scripts/build.sh" >&2
  exit 1
fi
module_cache=$(go env GOMODCACHE)

# The container owns all test data and executables. No runtime data is mounted,
# and the only release server is the test's TLS listener on container loopback.
docker run --rm --network none --user 65532:65532 \
  --mount "type=bind,source=$root,target=/src,readonly" \
  --mount "type=bind,source=$module_cache,target=/go/pkg/mod,readonly" \
  --workdir /src --env CGO_ENABLED=0 --env GOTOOLCHAIN=local --env GOCACHE=/tmp/go-build-cache \
  golang:1.27.1-bookworm \
  go test -tags=e2e -count=1 -timeout 10m -v ./internal/integration/selfupdate
