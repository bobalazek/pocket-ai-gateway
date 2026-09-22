#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
project="pocket-ai-gateway-e2e-$$"
export POCKET_AI_GATEWAY_E2E_PORT=${POCKET_AI_GATEWAY_E2E_PORT:-18082}
export POCKET_AI_GATEWAY_E2E_RESTORE_PORT=${POCKET_AI_GATEWAY_E2E_RESTORE_PORT:-18083}
compose=(docker compose -f "$root/compose.test.yaml" -p "$project")
mode=${1:-local}
case "$mode" in
  local) ;;
  --s3)
    command -v jq >/dev/null
    command -v openssl >/dev/null
    compose+=(-f "$root/compose.s3-test.yaml")
    ;;
  *) echo "Usage: $0 [--s3]" >&2; exit 1 ;;
esac
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

if [[ "$mode" == --s3 ]]; then
  "${compose[@]}" up --detach s3
  s3_base="http://$("${compose[@]}" port gateway 9000)"
  for _ in {1..120}; do
    if curl --silent --fail --max-time 2 "$s3_base/minio/health/ready" >/dev/null; then break; fi
    sleep 0.5
  done
  curl --silent --show-error --fail --max-time 5 "$s3_base/minio/health/ready" >/dev/null
  s3=(curl --silent --show-error --fail --max-time 60 --aws-sigv4 aws:amz:us-east-1:s3 --user pocket-e2e-access:pocket-e2e-secret)
  "${s3[@]}" --request PUT "$s3_base/pocket-e2e" >/dev/null

  curl --silent --fail --cookie "$scratch/cookies" "$base/api/v1/admin/settings" > "$scratch/settings.json"
  revision=$(jq -er '.settings.revision' "$scratch/settings.json")
  jq '.settings | .backup_destination="s3" | .s3_endpoint="http://127.0.0.1:9000" |
    .s3_bucket="pocket-e2e" | .s3_prefix="nightly +%/č" |
    .s3_access_key_env="POCKET_E2E_S3_ACCESS" | .s3_secret_key_env="POCKET_E2E_S3_SECRET"' \
    "$scratch/settings.json" > "$scratch/s3-settings.json"
  curl --silent --show-error --fail --max-time 60 --cookie "$scratch/cookies" \
    --header "Content-Type: application/json" --header "Origin: $base" --header "X-CSRF-Token: $csrf" \
    --header "If-Match: $revision" --request PATCH --data-binary "@$scratch/s3-settings.json" \
    "$base/api/v1/admin/settings" >/dev/null
fi

curl --silent --show-error --fail-with-body --max-time 60 --cookie "$scratch/cookies" \
  --header "Origin: $base" --header "X-CSRF-Token: $csrf" --request POST \
  "$base/api/v1/admin/backups" > "$scratch/backup.json"
archive=$(sed -n 's/.*"archive_name":"\([^"]*\)".*/\1/p' "$scratch/backup.json")
test -n "$archive"

if [[ "$mode" == --s3 ]]; then
  "${s3[@]}" "$s3_base/pocket-e2e/nightly%20%2B%25/%C4%8D/$archive" --output "$scratch/$archive"
  test "$(openssl dgst -sha256 "$scratch/$archive" | awk '{print $NF}')" = "$(jq -er '.backup.checksum' "$scratch/backup.json")"
  test "$(wc -c < "$scratch/$archive" | tr -d ' ')" = "$(jq -er '.backup.size_bytes' "$scratch/backup.json")"

  # A rejected upload must leave a visible failed job, while the instance stays ready.
  jq '.s3_secret_key_env="POCKET_E2E_S3_ACCESS"' "$scratch/s3-settings.json" > "$scratch/invalid-settings.json"
  curl --silent --show-error --fail --max-time 60 --cookie "$scratch/cookies" \
    --header "Content-Type: application/json" --header "Origin: $base" --header "X-CSRF-Token: $csrf" \
    --header "If-Match: $((revision + 1))" --request PATCH --data-binary "@$scratch/invalid-settings.json" \
    "$base/api/v1/admin/settings" >/dev/null
  status=$(curl --silent --show-error --max-time 60 --cookie "$scratch/cookies" \
    --header "Origin: $base" --header "X-CSRF-Token: $csrf" --request POST \
    --output "$scratch/failed.json" --write-out '%{http_code}' "$base/api/v1/admin/backups")
  test "$status" = 422
  jq -e '.error.code == "backup_configuration" and .error.message == "S3 upload returned HTTP 403"' "$scratch/failed.json" >/dev/null
  curl --silent --fail --max-time 10 --cookie "$scratch/cookies" "$base/api/v1/admin/backups" |
    jq -e '.data[0].state == "failed" and .data[1].state == "succeeded"' >/dev/null
  curl --silent --fail --max-time 10 "$base/readyz" >/dev/null

  # The upload removes its staging copy. Restore only the downloaded object.
  chmod 644 "$scratch/$archive"
  "${compose[@]}" cp "$scratch/$archive" "gateway:/data_backups/$archive"
fi

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

echo "Docker backup/restore E2E passed ($mode)"
