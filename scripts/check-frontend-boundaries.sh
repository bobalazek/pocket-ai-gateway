#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if find "$root/web/src" -type f \( -name '*.ts' -o -name '*.tsx' \) ! -path "$root/web/src/lib/api-client.ts" -exec grep -n -E '(^|[^[:alnum:]_])fetch[[:space:]]*\(' {} +; then
  echo "Use web/src/lib/api-client.ts instead of direct fetch calls." >&2
  exit 1
fi

if grep -R -n -E --include='*.ts' --include='*.tsx' '(^|[^[:alnum:]_])(localStorage|sessionStorage)([^[:alnum:]_]|$)' "$root/web/src"; then
  echo "Do not persist dashboard credentials or secrets in browser storage." >&2
  exit 1
fi
