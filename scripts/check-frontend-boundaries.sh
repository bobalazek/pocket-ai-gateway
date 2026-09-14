#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if rg --line-number --glob '*.ts' --glob '*.tsx' --glob '!**/lib/api-client.ts' '\bfetch[[:space:]]*\(' "$root/web/src"; then
  echo "Use web/src/lib/api-client.ts instead of direct fetch calls." >&2
  exit 1
fi

if rg --line-number --glob '*.ts' --glob '*.tsx' '\b(localStorage|sessionStorage)\b' "$root/web/src"; then
  echo "Do not persist dashboard credentials or secrets in browser storage." >&2
  exit 1
fi
