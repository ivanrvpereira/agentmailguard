#!/usr/bin/env bash
# Writes the license notices of everything compiled into amg and amgd for GOOS into OUT_DIR, for the release
# archives. Fails if a dependency has a license that is not on the allowed list.
# Needs go-licenses (github.com/google/go-licenses/v2) on PATH or in GOBIN, and, for linux, the musl package.
# Usage: scripts/release/licenses.sh GOOS OUT_DIR
set -euo pipefail
usage="usage: licenses.sh GOOS OUT_DIR"
goos="${1:?$usage}"
out="${2:?$usage}"
cd "$(dirname "$0")/../.."
allowed="Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT"
self="github.com/ivanrvpereira/agentmailguard"
gobin="$(go env GOBIN)"
tool="$(command -v go-licenses || echo "${gobin:-$(go env GOPATH)/bin}/go-licenses")"
test -x "$tool" || { echo "licenses.sh: go-licenses not found" >&2; exit 1; }

export GOOS="$goos" CGO_ENABLED=1
rm -rf "$out"
"$tool" check ./cmd/amg ./cmd/amgd --ignore "$self" --allowed_licenses="$allowed"
"$tool" save ./cmd/amg ./cmd/amgd --ignore "$self" --save_path="$out"
mkdir -p "$out/go"
cp "$(go env GOROOT)/LICENSE" "$out/go/LICENSE"

{
  echo "Third-party notices for agentmailguard ($goos)"
  echo
  echo "amg and amgd contain the Go standard library (go/LICENSE) and these modules, each under its license in this folder:"
  echo
  "$tool" report ./cmd/amg ./cmd/amgd --ignore "$self" 2>/dev/null | LC_ALL=C sort | awk -F, '{ printf "- %s: %s\n", $1, $3 }'
  echo
  echo "amgd also contains SQLite (through github.com/mattn/go-sqlite3), which is in the public domain: https://sqlite.org/copyright.html"
} > "$out/README.txt"

if [ "$goos" = linux ]; then
  # The static Linux amgd links musl.
  musl="/usr/share/doc/musl/copyright"
  test -f "$musl" || { echo "licenses.sh: $musl not found; install the musl package" >&2; exit 1; }
  mkdir -p "$out/musl"
  cp "$musl" "$out/musl/COPYRIGHT"
  echo "The static amgd also contains the musl C library (musl/COPYRIGHT)." >> "$out/README.txt"
fi
echo "$out"
