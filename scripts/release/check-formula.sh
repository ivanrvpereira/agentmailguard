#!/usr/bin/env bash
# Checks a filled-in formula with brew style and brew audit --strict in a temporary tap, then removes the tap.
# It changes nothing else in Homebrew. Leave HOMEBREW_NO_INSTALL_FROM_API unset: with it, brew audit clones
# homebrew/core (over 1 GB).
# Usage: scripts/release/check-formula.sh FORMULA_FILE
set -euo pipefail
formula="${1:?usage: check-formula.sh FORMULA_FILE}"
tap="agentmailguard-check/local"
export HOMEBREW_NO_AUTO_UPDATE=1
unset HOMEBREW_NO_INSTALL_FROM_API

if brew tap | grep -qx "$tap"; then
  echo "Tap $tap exists already; remove it with: brew untap $tap" >&2
  exit 1
fi
brew tap-new --no-git "$tap" >/dev/null
trap 'brew untap --force "$tap" >/dev/null' EXIT
install -m 0644 "$formula" "$(brew --repository "$tap")/Formula/agentmailguard.rb"
brew trust --formula "$tap/agentmailguard" >/dev/null
brew style "$tap/agentmailguard"
brew audit --strict --formula "$tap/agentmailguard"
