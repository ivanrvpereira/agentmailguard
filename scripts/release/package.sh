#!/usr/bin/env bash
# Packs the amg and amgd binaries in BIN_DIR with the documents into DIST_DIR/agentmailguard_VERSION_OS_ARCH.tar.gz.
# Usage: scripts/release/package.sh TAG OS ARCH BIN_DIR DIST_DIR
set -euo pipefail
usage="usage: package.sh TAG OS ARCH BIN_DIR DIST_DIR"
tag="${1:?$usage}"
os="${2:?$usage}"
arch="${3:?$usage}"
bin="${4:?$usage}"
dist="${5:?$usage}"
bin="$(cd "$bin" && pwd)"
mkdir -p "$dist"
dist="$(cd "$dist" && pwd)"
cd "$(dirname "$0")/../.."

name="agentmailguard_${tag#v}_${os}_${arch}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/$name"
cp "$bin/amg" "$bin/amgd" LICENSE README.md CHANGELOG.md SECURITY.md "$stage/$name/"
cp -R docs skills "$stage/$name/"
# COPYFILE_DISABLE keeps macOS tar from adding AppleDouble (._*) files.
COPYFILE_DISABLE=1 tar -czf "$dist/$name.tar.gz" -C "$stage" "$name"
echo "$dist/$name.tar.gz"
