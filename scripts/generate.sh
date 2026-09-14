#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}

cd "$root"
go tool sqlc generate
