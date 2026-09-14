#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}
output="$root/dist/cross"
mkdir -p "$output"

targets=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
)

for target in "${targets[@]}"; do
  read -r os arch <<<"$target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$output/pocket-ai-gateway-$os-$arch" "$root/cmd/pocket-ai-gateway"
done
