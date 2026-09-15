#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}
output="$root/dist/cross"
gateway_version=${VERSION:-dev}
rm -rf "$output"
mkdir -p "$output"

targets=(
  "linux amd64"
  "linux arm64"
)

for target in "${targets[@]}"; do
  read -r os arch <<<"$target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X=main.version=$gateway_version" -o "$output/pocket-ai-gateway-$os-$arch" "$root/cmd/pocket-ai-gateway"
done
