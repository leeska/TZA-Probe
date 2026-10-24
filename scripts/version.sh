#!/bin/sh
# Print the source build version without modifying the checkout.
set -eu
root=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
tag=$(git -C "$root" describe --exact-match --tags --match 'v[0-9]*' HEAD 2>/dev/null || true)
if printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
 printf '%s\n' "$tag"
 exit 0
fi
base=$(sed -n 's/^var Version = "\([^"]*\)".*/\1/p' "$root/internal/model/model.go")
base=${base%%-*}
printf '%s\n' "$base" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'invalid source version' >&2; exit 1; }
sha=${2:-$(git -C "$root" rev-parse --short HEAD 2>/dev/null || true)}
printf '%s-dev%s\n' "$base" "${sha:++$sha}"
