#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
project="pocket-ai-gateway-e2e-$$"
export POCKET_AI_GATEWAY_E2E_PORT=${POCKET_AI_GATEWAY_E2E_PORT:-18082}
export POCKET_AI_GATEWAY_E2E_RESTORE_PORT=${POCKET_AI_GATEWAY_E2E_RESTORE_PORT:-18083}
compose=(docker compose -f "$root/compose.test.yaml" -p "$project")
scratch=$(mktemp -d "${TMPDIR:-/tmp}/pocket-ai-gateway-compose-e2e.XXXXXX")

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$scratch"
}
trap cleanup EXIT

wait_ready() {
  local base=$1
  for _ in {1..120}; do
    if curl --silent --fail "$base/readyz" >/dev/null 2>&1; then
      return
    fi
    sleep 0.5
  done
  "${compose[@]}" logs >&2
  return 1
}

base="http://127.0.0.1:$POCKET_AI_GATEWAY_E2E_PORT"
restored_base="http://127.0.0.1:$POCKET_AI_GATEWAY_E2E_RESTORE_PORT"

"${compose[@]}" up --build --detach gateway
wait_ready "$base"

curl --silent --fail --cookie-jar "$scratch/cookies" \
  --header "Content-Type: application/json" --header "Origin: $base" \
  --data '{"display_name":"E2E Owner","email":"owner@example.test","password":"correct-horse-battery"}' \
  "$base/api/v1/auth/setup/claim" | grep -q '"role":"owner"'
curl --silent --fail "$base/api/v1/auth/setup/status" | grep -q '"setup_required":false'

csrf=$(awk '$6 == "pocket_ai_gateway_csrf" { print $7 }' "$scratch/cookies")
test -n "$csrf"
archive=$(curl --silent --fail --cookie "$scratch/cookies" \
  --header "Origin: $base" --header "X-CSRF-Token: $csrf" --request POST \
  "$base/api/v1/admin/backups" | sed -n 's/.*"archive_name":"\([^"]*\)".*/\1/p')
test -n "$archive"

"${compose[@]}" stop gateway
"${compose[@]}" run --rm restored restore-backup --archive "/data_backups/$archive" --data-dir /data/restored >/dev/null
"${compose[@]}" up --detach restored
wait_ready "$restored_base"

curl --silent --fail "$restored_base/api/v1/auth/setup/status" | grep -q '"setup_required":false'
curl --silent --fail --cookie-jar "$scratch/restored-cookies" \
  --header "Content-Type: application/json" --header "Origin: $restored_base" \
  --data '{"email":"owner@example.test","password":"correct-horse-battery"}' \
  "$restored_base/api/v1/auth/login" | grep -q '"role":"owner"'

"${compose[@]}" restart restored >/dev/null
wait_ready "$restored_base"
curl --silent --fail --cookie "$scratch/restored-cookies" \
  "$restored_base/api/v1/auth/session" | grep -q '"email":"owner@example.test"'

echo "Docker backup/restore E2E passed"
