#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}
gateway_version=${VERSION:-dev}
export NEXT_TELEMETRY_DISABLED=1

cd "$root"
if [[ ! -x web/node_modules/.bin/next || ! -x web/node_modules/.bin/tsc ]]; then
  CI=true pnpm --dir web install --frozen-lockfile
fi
mkdir -p web/public
cp llms.txt web/public/llms.txt
pnpm --dir web build
mkdir -p dist
go build -trimpath -ldflags "-s -w -X=main.version=$gateway_version" -o dist/pocket-ai-gateway ./cmd/pocket-ai-gateway
