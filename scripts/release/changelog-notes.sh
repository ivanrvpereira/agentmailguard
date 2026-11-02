#!/usr/bin/env bash
# Prints the release notes for TAG: the body of its "## [X.Y.Z]" section in CHANGELOG.md.
# A prerelease tag (vX.Y.Z-suffix) without its own section uses the "## [Unreleased]" section.
# Fails when the section is missing or empty, so a release cannot go out without notes.
# Usage: scripts/release/changelog-notes.sh TAG [CHANGELOG]
set -euo pipefail
usage="usage: changelog-notes.sh TAG [CHANGELOG]"
tag="${1:?$usage}"
changelog="${2:-$(dirname "$0")/../../CHANGELOG.md}"
version="${tag#v}"

section() {
  # The body runs from the heading to the next "## " heading or the link definitions at the end.
  awk -v want="## [$1]" '
    index($0, want) == 1 { on = 1; next }
    on && (/^## / || /^\[[^]]+\]: /) { exit }
    on { print }
  ' "$changelog" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
}

notes="$(section "$version")"
if [ -z "$notes" ] && [[ "$version" == *-* ]]; then
  notes="$(section Unreleased)"
fi
if [ -z "$notes" ]; then
  echo "changelog-notes.sh: CHANGELOG.md has no notes for $version: add a '## [$version] - YYYY-MM-DD' section" >&2
  exit 1
fi
printf '%s\n' "$notes"
