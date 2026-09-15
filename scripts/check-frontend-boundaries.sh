#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if find "$root/web/src" -type f \( -name '*.ts' -o -name '*.tsx' \) ! -path "$root/web/src/lib/api-client.ts" -exec grep -n -E '(^|[^[:alnum:]_])fetch[[:space:]]*\(' {} +; then
  echo "Use web/src/lib/api-client.ts instead of direct fetch calls." >&2
  exit 1
fi

if find "$root/web/src/app" -type f -name 'page.tsx' -exec grep -n -E 'use client|use(State|Effect|Ref)|@/lib/api-client|@/features/.*/clients/' {} +; then
  echo "Keep app/**/page.tsx as thin route files that render feature components." >&2
  exit 1
fi

if find "$root/web/src/features" -type f \( -path '*/components/*.ts' -o -path '*/components/*.tsx' -o -path '*/hooks/*.ts' -o -path '*/hooks/*.tsx' \) -exec grep -n -E '@/features/.*/clients/|@/lib/api-client' {} +; then
  echo "Feature components and hooks must use the pocketAIGatewayAdmin facade." >&2
  exit 1
fi

if find "$root/web/src" -type f \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.ts' ! -path "$root/web/src/lib/pocket-ai-gateway-admin.client.ts" -exec grep -n -E '@/features/.*/clients/' {} +; then
  echo "Only pocket-ai-gateway-admin.client.ts may compose feature clients." >&2
  exit 1
fi

if find "$root/web/src" -type f \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.ts' ! -path "$root/web/src/lib/api-client.ts" ! -path "$root/web/src/lib/pocket-ai-gateway-admin.client.ts" ! -path '*/features/*/clients/*.ts' -exec grep -n -E '@/lib/api-client' {} +; then
  echo "Only feature clients may call the shared API transport." >&2
  exit 1
fi

if find "$root/web/src/features" -type f -path '*/components/*.tsx' -exec grep -n -E 'use(State|Effect|Ref)' {} +; then
  echo "Move feature state and effects into feature hooks." >&2
  exit 1
fi

if grep -R -n -E --include='*.ts' --include='*.tsx' '(^|[^[:alnum:]_])(localStorage|sessionStorage)([^[:alnum:]_]|$)' "$root/web/src"; then
  echo "Do not persist dashboard credentials or secrets in browser storage." >&2
  exit 1
fi
