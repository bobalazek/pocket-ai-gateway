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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$scratch/pocket-ai-gateway-linux-amd64" ./cmd/pocket-ai-gateway
go run ./scripts/notices -binary "$scratch/pocket-ai-gateway-linux-amd64" -web web -output "$scratch/THIRD_PARTY_NOTICES.md"
cmp THIRD_PARTY_NOTICES.md "$scratch/THIRD_PARTY_NOTICES.md"

unformatted=$(gofmt -l cmd internal scripts web/embed.go)
if [[ -n "$unformatted" ]]; then
  echo "Go files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi

cp -R internal/storage/sqlc "$scratch/sqlc"
./scripts/generate.sh
diff -ru "$scratch/sqlc" internal/storage/sqlc

go mod tidy -diff
go vet ./...
go tool staticcheck ./...
go test ./...
pnpm --dir web typecheck
pnpm --dir web test

if [[ "${POCKET_AI_GATEWAY_QUICK_VERIFY:-}" != "1" ]]; then
  go test -race -timeout 15m ./internal/storage ./internal/server ./internal/app ./internal/selfupdate ./internal/features/auth ./internal/features/users ./internal/features/keys ./internal/features/usage ./internal/features/providers ./internal/features/operations ./internal/features/mediajobs ./internal/gateway ./internal/protocol
  ./scripts/smoke.sh "$root/dist/pocket-ai-gateway"
fi
