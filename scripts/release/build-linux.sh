#!/usr/bin/env bash
# Builds static Linux binaries of amg and amgd for this machine's architecture into OUT_DIR, and checks that
# they run without a C library. Needs musl-gcc (Debian and Ubuntu: musl-tools) and, for the run check, Docker.
# Usage: scripts/release/build-linux.sh TAG OUT_DIR
set -euo pipefail
tag="${1:?usage: build-linux.sh TAG OUT_DIR}"
out="${2:?usage: build-linux.sh TAG OUT_DIR}"
version_flag="github.com/ivanrvpereira/agentmailguard/internal/version.version=$tag"
cd "$(dirname "$0")/../.."
mkdir -p "$out"

# amgd links SQLite through cgo. Linking it statically against musl makes one binary that runs on any Linux
# distribution, whatever its C library. amg is pure Go and static already.
CGO_ENABLED=1 CC=musl-gcc go build -trimpath -tags osusergo,netgo,sqlite_omit_load_extension \
  -ldflags "-s -w -linkmode external -extldflags -static -X $version_flag" -o "$out/amgd" ./cmd/amgd
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X $version_flag" -o "$out/amg" ./cmd/amg

for b in amg amgd; do
  file "$out/$b"
  file "$out/$b" | grep -q 'statically linked'
  got="$(docker run --rm -v "$(cd "$out" && pwd):/b:ro" busybox:stable "/b/$b" --version)"
  test "$got" = "$b version $tag" || { echo "$b --version in busybox: $got" >&2; exit 1; }
done
