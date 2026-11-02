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
  # The body runs from the heading to the next "## " heading, or to the link definitions at the end of the
  # file: only the definitions after the last other text end it, not one inside a section.
  awk -v want="## [$1]" '
    { line[NR] = $0 }
    $0 !~ /^\[[^]]+\]: / && $0 !~ /^[[:space:]]*$/ { last = NR }
    END {
      for (i = 1; i <= NR; i++) {
        if (!on && index(line[i], want) == 1) { on = 1; continue }
        if (on && (line[i] ~ /^## / || i > last)) break
        if (on) print line[i]
      }
    }
  ' "$changelog" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
}

# A section of only spaces has no notes.
blank() { [ -z "${1//[[:space:]]/}" ]; }

notes="$(section "$version")"
if blank "$notes" && [[ "$version" == *-* ]]; then
  notes="$(section Unreleased)"
fi
if blank "$notes"; then
  echo "changelog-notes.sh: CHANGELOG.md has no notes for $version: add a '## [$version] - YYYY-MM-DD' section" >&2
  exit 1
fi
printf '%s\n' "$notes"
