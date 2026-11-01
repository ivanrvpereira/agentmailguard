#!/usr/bin/env bash
# Builds unsigned macOS binaries of amg and amgd for arm64 and amd64 into OUT_DIR/arm64 and OUT_DIR/amd64, and
# checks their architecture and minimum macOS version. Runs on an Apple silicon or Intel Mac with Xcode tools.
# Usage: scripts/release/build-macos.sh TAG OUT_DIR
set -euo pipefail
tag="${1:?usage: build-macos.sh TAG OUT_DIR}"
out="${2:?usage: build-macos.sh TAG OUT_DIR}"
version_flag="github.com/ivanrvpereira/agentmailguard/internal/version.version=$tag"
# Oldest macOS that the binaries run on: the minimum of the Go release that builds them. The README's Install
# section states it too. When Go needs a newer macOS, the link check below fails: raise this value.
macos_min="13.0"
cd "$(dirname "$0")/../.."
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

for arch in arm64 amd64; do
  mkdir -p "$out/$arch"
  # Without a deployment target, cgo builds for this machine's macOS and the binary refuses older releases.
  # The flags go into CGO_CFLAGS and CGO_LDFLAGS, which the Go build cache tracks.
  CGO_ENABLED=1 GOARCH=$arch CGO_CFLAGS="-O2 -g -mmacosx-version-min=$macos_min" CGO_LDFLAGS="-mmacosx-version-min=$macos_min" \
    go build -trimpath -ldflags "-s -w -X $version_flag" -o "$out/$arch/amgd" ./cmd/amgd 2> "$log" || { cat "$log" >&2; exit 1; }
  cat "$log" >&2
  if grep -q "built for newer 'macOS' version" "$log"; then
    echo "Objects need a newer macOS than $macos_min; raise macos_min in $0" >&2
    exit 1
  fi
  CGO_ENABLED=0 GOARCH=$arch go build -trimpath -ldflags "-s -w -X $version_flag" -o "$out/$arch/amg" ./cmd/amg

  want=arm64; [ "$arch" = amd64 ] && want=x86_64
  for b in amg amgd; do
    test "$(lipo -archs "$out/$arch/$b")" = "$want"
    minos="$(vtool -show-build "$out/$arch/$b" | awk '$1 == "minos" { print $2 }')"
    echo "$out/$arch/$b: minos $minos"
    test "$(printf '%s\n%s\n' "$minos" "$macos_min" | sort -V | tail -1)" = "$macos_min"
  done
done
