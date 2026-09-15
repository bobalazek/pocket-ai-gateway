#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/pocket-ai-gateway" >&2
  exit 2
fi

binary=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
scratch=$(mktemp -d "${TMPDIR:-/tmp}/pocket-ai-gateway-smoke.XXXXXX")
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT

cp "$binary" "$scratch/pocket-ai-gateway"
cd "$scratch"
export POCKET_AI_GATEWAY_BACKUP_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
./pocket-ai-gateway serve --listen 127.0.0.1:0 --data-dir "$scratch/data" >"$scratch/server.log" 2>&1 &
server_pid=$!

address=""
for _ in {1..100}; do
  address=$(rg -o 'listen=127\.0\.0\.1:[0-9]+' "$scratch/server.log" 2>/dev/null | tail -n 1 | cut -d= -f2 || true)
  [[ -n "$address" ]] && break
  if ! kill -0 "$server_pid" 2>/dev/null; then
    cat "$scratch/server.log" >&2
    exit 1
  fi
  sleep 0.05
done
if [[ -z "$address" ]]; then
  echo "server did not become ready" >&2
  exit 1
fi
base="http://$address"

expect_status() {
  local expected=$1
  local url=$2
  local actual
  actual=$(curl --silent --output /dev/null --write-out '%{http_code}' "$url")
  if [[ "$actual" != "$expected" ]]; then
    echo "$url returned $actual, expected $expected" >&2
    exit 1
  fi
}

expect_status 200 "$base/healthz"
expect_status 200 "$base/llms.txt"
expect_status 200 "$base/_/"
expect_status 200 "$base/_/setup/"
expect_status 200 "$base/_/status/?id=request_123"
expect_status 200 "$base/_/login/"
expect_status 200 "$base/_/activate/"
expect_status 200 "$base/_/account/"
expect_status 200 "$base/_/users/"
expect_status 200 "$base/_/keys/"
expect_status 200 "$base/_/usage/"
expect_status 200 "$base/_/audit/"
expect_status 200 "$base/_/settings/"
expect_status 200 "$base/readyz"
expect_status 404 "$base/_/missing/"
expect_status 404 "$base/api/openai/v1/missing"
expect_status 404 "$base/api/anthropic/v1/missing"
expect_status 404 "$base/api/gemini/v1beta/missing"
expect_status 404 "$base/v1/missing"

curl --silent "$base/api/openai/v1/missing" | rg --quiet '"code":"not_found"'
curl --silent "$base/llms.txt" | rg --quiet '^# Pocket AI Gateway$'
curl --silent "$base/api/v1/auth/setup/status" | rg --quiet '"setup_required":true'
setup_code=$(tr -d '\n' < "$scratch/data/setup-code")
[[ -n "$setup_code" ]]
if rg --fixed-strings --quiet -- "$setup_code" "$scratch/server.log"; then
  echo "setup code leaked into process logs" >&2
  exit 1
fi
curl --silent --fail --cookie-jar "$scratch/cookies" --header "Content-Type: application/json" --header "Origin: $base" \
  --data "{\"setup_code\":\"$setup_code\",\"display_name\":\"Smoke Owner\",\"email\":\"owner@example.test\",\"password\":\"correct-horse-battery\"}" \
  "$base/api/v1/auth/setup/claim" | rg --quiet '"role":"owner"'
curl --silent "$base/api/v1/auth/setup/status" | rg --quiet '"setup_required":false'
curl --silent --cookie "$scratch/cookies" "$base/api/v1/auth/session" | rg --quiet '"email":"owner@example.test"'
csrf=$(awk '$6 == "pocket_ai_gateway_csrf" { print $7 }' "$scratch/cookies")
[[ -n "$csrf" ]]
curl --silent --fail --cookie "$scratch/cookies" "$base/api/v1/usage" | rg --quiet '"known_cost_usd":"0"'
curl --silent --fail --cookie "$scratch/cookies" "$base/api/v1/admin/diagnostics" | rg --quiet '"backup_key_configured":true'
curl --silent --fail --cookie "$scratch/cookies" --header "Origin: $base" --header "X-CSRF-Token: $csrf" --request POST \
  "$base/api/v1/admin/backups" | rg --quiet '"state":"succeeded"'
test -f "$scratch/data_backups/"*.pagbak
curl --silent --fail --cookie "$scratch/cookies" --header "Content-Type: application/json" --header "Origin: $base" --header "X-CSRF-Token: $csrf" \
  --data '{"scope_kind":"instance","scope_id":"","metric":"requests","algorithm":"quota","period":"day","window_seconds":0,"limit_units":100,"limit_usd":"","refill_units":0,"refill_interval_ms":0}' \
  "$base/api/v1/admin/policies" | rg --quiet '"metric":"requests"'
user_response=$(curl --silent --fail --cookie "$scratch/cookies" --header "Content-Type: application/json" --header "Origin: $base" --header "X-CSRF-Token: $csrf" \
  --data '{"display_name":"Smoke Member","email":"member@example.test","role":"member"}' "$base/api/v1/admin/users")
activation_code=$(printf '%s' "$user_response" | rg -o '"activation_code":"[^"]+"' | cut -d'"' -f4)
[[ -n "$activation_code" ]]
if rg --fixed-strings --quiet -- "$activation_code" "$scratch/server.log"; then
  echo "activation code leaked into process logs" >&2
  exit 1
fi
key_response=$(curl --silent --fail --cookie "$scratch/cookies" --header "Content-Type: application/json" --header "Origin: $base" --header "X-CSRF-Token: $csrf" \
  --data '{"label":"Smoke key","scopes":["chat:generate"],"model_patterns":["gpt-*"],"connection_ids":[],"expires_at":""}' "$base/api/v1/keys")
key_id=$(printf '%s' "$key_response" | rg -o '"id":"key_[^"]+"' | head -n1 | cut -d'"' -f4)
key_secret=$(printf '%s' "$key_response" | rg -o '"secret":"[^"]+"' | cut -d'"' -f4)
[[ -n "$key_id" && -n "$key_secret" ]]
if rg --fixed-strings --quiet -- "$key_secret" "$scratch/server.log"; then
  echo "API key secret leaked into process logs" >&2
  exit 1
fi
curl --silent --fail --cookie "$scratch/cookies" --header "Origin: $base" --header "X-CSRF-Token: $csrf" --header 'If-Match: "1"' --request POST \
  "$base/api/v1/keys/$key_id/rotate" | rg --quiet '"secret":"pag_'
curl --silent --fail --cookie "$scratch/cookies" --header "Origin: $base" --header "X-CSRF-Token: $csrf" --header 'If-Match: "2"' --request DELETE \
  "$base/api/v1/keys/$key_id"
curl --silent --fail --cookie "$scratch/cookies" --header "Origin: $base" --header "X-CSRF-Token: $csrf" --request POST "$base/api/v1/auth/logout"
curl --silent --fail --cookie-jar "$scratch/login-cookies" --header "Content-Type: application/json" --header "Origin: $base" \
  --data '{"email":"owner@example.test","password":"correct-horse-battery"}' "$base/api/v1/auth/login" | rg --quiet '"role":"owner"'
curl --silent --fail --cookie "$scratch/login-cookies" "$base/api/v1/auth/sessions" | rg --quiet '"current":true'
asset=$(curl --silent "$base/_/" | rg -o '/_/_next/static/[^" ]+\.(css|js)' | head -n 1)
[[ -n "$asset" ]]
expect_status 200 "$base$asset"

if ./pocket-ai-gateway serve --listen 127.0.0.1:0 --data-dir "$scratch/data" >"$scratch/second.log" 2>&1; then
  echo "second process unexpectedly acquired the data directory" >&2
  exit 1
fi
rg --quiet 'already in use' "$scratch/second.log"

kill -TERM "$server_pid"
wait "$server_pid"
server_pid=""
rg --quiet 'gateway stopped' "$scratch/server.log"

./pocket-ai-gateway snapshot --data-dir "$scratch/data" --output "$scratch/snapshot" >/dev/null
./pocket-ai-gateway restore --snapshot "$scratch/snapshot" --data-dir "$scratch/restored" >/dev/null
./pocket-ai-gateway backup --data-dir "$scratch/data" --output "$scratch/backup.pagbak" >/dev/null
./pocket-ai-gateway restore-backup --archive "$scratch/backup.pagbak" --data-dir "$scratch/restored-backup" >/dev/null
./pocket-ai-gateway version | rg --quiet '.+'
