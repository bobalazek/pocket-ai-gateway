#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/pocket-ai-gateway-go-cache}
scratch=$(mktemp -d "${TMPDIR:-/tmp}/pocket-ai-gateway-verify.XXXXXX")
trap 'rm -rf "$scratch"' EXIT

cd "$root"
./scripts/check-frontend-boundaries.sh
go run ./scripts/check-openapi.go docs/reference/openapi.yaml
./scripts/build.sh

unformatted=$(gofmt -l cmd internal web/embed.go)
if [[ -n "$unformatted" ]]; then
  echo "Go files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi

cp -R internal/storage/sqlc "$scratch/sqlc"
./scripts/generate.sh
diff -ru "$scratch/sqlc" internal/storage/sqlc

go vet ./...
go test ./...
go test -race ./internal/storage ./internal/server ./internal/app ./internal/features/auth ./internal/features/users ./internal/features/keys
pnpm --dir web typecheck
pnpm --dir web test
./scripts/cross-build.sh
./scripts/smoke.sh "$root/dist/pocket-ai-gateway"
