#!/usr/bin/env bash
# Prints the Homebrew formula for a release: packaging/homebrew/agentmailguard.rb with the version and the
# sha256 of each archive from CHECKSUMS (sha256sum output for agentmailguard_VERSION_OS_ARCH.tar.gz).
# Usage: scripts/release/render-formula.sh VERSION CHECKSUMS > agentmailguard.rb   (VERSION without the v)
set -euo pipefail
version="${1:?usage: render-formula.sh VERSION CHECKSUMS}"
checksums="${2:?usage: render-formula.sh VERSION CHECKSUMS}"
template="$(dirname "$0")/../../packaging/homebrew/agentmailguard.rb"

sha() {
  local s
  s="$(awk -v f="agentmailguard_${version}_$1.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$checksums")"
  [[ "$s" =~ ^[0-9a-f]{64}$ ]] || { echo "No sha256 for $1 in $checksums" >&2; exit 1; }
  printf '%s' "$s"
}
# Assignments, not substitutions inside the sed arguments: only an assignment stops the script when sha fails.
darwin_arm64="$(sha darwin_arm64)"
darwin_amd64="$(sha darwin_amd64)"
linux_arm64="$(sha linux_arm64)"
linux_amd64="$(sha linux_amd64)"
formula="$(sed -e "s/{{VERSION}}/$version/g" \
  -e "s/{{SHA256_DARWIN_ARM64}}/$darwin_arm64/" -e "s/{{SHA256_DARWIN_AMD64}}/$darwin_amd64/" \
  -e "s/{{SHA256_LINUX_ARM64}}/$linux_arm64/" -e "s/{{SHA256_LINUX_AMD64}}/$linux_amd64/" "$template")"
if grep -q '{{' <<< "$formula"; then
  echo "Unfilled placeholder in the formula" >&2
  exit 1
fi
printf '%s\n' "$formula"
