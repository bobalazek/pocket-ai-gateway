#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
version=${1:-${VERSION:-}}
if [[ -z "$version" ]]; then
  echo "usage: scripts/package.sh VERSION" >&2
  exit 2
fi
export VERSION="$version"

"$root/scripts/build.sh"
"$root/scripts/cross-build.sh"

release="$root/dist/release"
rm -rf "$release"
mkdir -p "$release"
for binary in "$root"/dist/cross/pocket-ai-gateway-*; do
  name=$(basename "$binary")
  package="pocket-ai-gateway-${version#v}-${name#pocket-ai-gateway-}"
  cp "$binary" "$release/$package"
  staging=$(mktemp -d)
  cp "$binary" "$staging/"
  cp "$root/README.md" "$root/LICENSE" "$root/THIRD_PARTY_NOTICES.md" "$staging/"
  if [[ "$name" == *.exe ]]; then
    (cd "$staging" && zip -q "$release/$package.zip" ./*)
  else
    tar -C "$staging" -czf "$release/$package.tar.gz" .
  fi
  rm -rf "$staging"
done

if command -v sha256sum >/dev/null; then
  (cd "$release" && sha256sum ./* > SHA256SUMS)
else
  (cd "$release" && shasum -a 256 ./* > SHA256SUMS)
fi
