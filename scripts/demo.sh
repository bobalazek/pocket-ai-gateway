#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}
cd "$root"
./scripts/build.sh
demo_bin=$(mktemp -d "${TMPDIR:-/tmp}/pocket-ai-gateway-demo-bin.XXXXXX")
trap 'rm -rf "$demo_bin"' EXIT
go build -o "$demo_bin/demo" ./scripts/demo
"$demo_bin/demo" "$@"
