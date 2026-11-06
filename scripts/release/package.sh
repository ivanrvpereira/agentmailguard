#!/usr/bin/env bash
# Packs the amg and amgd binaries in BIN_DIR with the documents, the service file of the OS (a systemd unit or a
# launchd plist), and the third-party notices in LICENSES_DIR (from licenses.sh) into
# DIST_DIR/agentmailguard_VERSION_OS_ARCH.tar.gz.
# Usage: scripts/release/package.sh TAG OS ARCH BIN_DIR DIST_DIR LICENSES_DIR
set -euo pipefail
usage="usage: package.sh TAG OS ARCH BIN_DIR DIST_DIR LICENSES_DIR"
tag="${1:?$usage}"
os="${2:?$usage}"
arch="${3:?$usage}"
bin="${4:?$usage}"
dist="${5:?$usage}"
licenses="${6:?$usage}"
licenses="$(cd "$licenses" && pwd)"
test -f "$licenses/README.txt" || { echo "package.sh: no third-party notices in $licenses" >&2; exit 1; }
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
mkdir "$stage/$name/service"
case "$os" in
  linux) cp packaging/systemd/amgd.service "$stage/$name/service/" ;;
  darwin) cp packaging/launchd/io.github.ivanrvpereira.agentmailguard.plist "$stage/$name/service/" ;;
  *) echo "package.sh: no service file for $os" >&2; exit 1 ;;
esac
cp -R "$licenses" "$stage/$name/THIRD_PARTY_LICENSES"
# COPYFILE_DISABLE keeps macOS tar from adding AppleDouble (._*) files.
COPYFILE_DISABLE=1 tar -czf "$dist/$name.tar.gz" -C "$stage" "$name"
echo "$dist/$name.tar.gz"
