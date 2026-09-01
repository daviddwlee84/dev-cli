#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_TEST"' EXIT

formula="$TMPDIR_TEST/dev-cli.rb"
checksum="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
"$ROOT/scripts/update-homebrew-formula.sh" \
  --source-sha256 "$checksum" \
  --output "$formula" \
  v9.8.7

grep -Fq 'refs/tags/v9.8.7.tar.gz' "$formula"
grep -Fq "sha256 \"$checksum\"" "$formula"
grep -Fq 'Version=v#{version}' "$formula"
if grep -Eq '@(VERSION|SHA256)@' "$formula"; then
  echo "rendered formula contains an unresolved placeholder" >&2
  exit 1
fi
if command -v ruby >/dev/null 2>&1; then
  ruby -c "$formula" >/dev/null
fi

if "$ROOT/scripts/update-homebrew-formula.sh" \
  --source-sha256 "$checksum" \
  --output "$TMPDIR_TEST/invalid.rb" \
  v9.8 >/dev/null 2>&1; then
  echo "invalid release tag was accepted" >&2
  exit 1
fi

if env -u HOMEBREW_TAP_TOKEN "$ROOT/scripts/update-homebrew-formula.sh" \
  --source-sha256 "$checksum" \
  v9.8.7 >/dev/null 2>&1; then
  echo "publishing without HOMEBREW_TAP_TOKEN was accepted" >&2
  exit 1
fi

echo "Homebrew formula renderer tests passed"
