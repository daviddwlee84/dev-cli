#!/usr/bin/env bash
# Refresh packaging/scoop/dev-cli.json from a freshly built SHA256SUMS, attach
# it to the GitHub release, and push it to the Scoop bucket repo when a token is
# configured. Called from release.yml with continue-on-error: a packaging
# hiccup must never fail an otherwise-good release.
set -euo pipefail

VERSION="${1:?usage: update-scoop-manifest.sh <vX.Y.Z> <SHA256SUMS path>}"
SUMS="${2:?SHA256SUMS path required}"
BARE="${VERSION#v}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="$ROOT/packaging/scoop/dev-cli.json"
BASE="https://github.com/daviddwlee84/dev-cli/releases/download"

hash_for() {
  local file="dev-cli_${VERSION}_windows_$1.zip"
  awk -v f="$file" '{ name = $2; sub(/^\*/, "", name); if (name == f) print $1 }' "$SUMS"
}

AMD64="$(hash_for amd64)"
ARM64="$(hash_for arm64)"
if [ -z "$AMD64" ] || [ -z "$ARM64" ]; then
  echo "no windows zip hashes in $SUMS; nothing to do" >&2
  exit 0
fi

tmp="$(mktemp)"
jq --arg v "$BARE" --arg amd "$AMD64" --arg arm "$ARM64" --arg base "$BASE" '
    .version = $v
  | .architecture["64bit"].url  = "\($base)/v\($v)/dev-cli_v\($v)_windows_amd64.zip"
  | .architecture["64bit"].hash = $amd
  | .architecture["arm64"].url  = "\($base)/v\($v)/dev-cli_v\($v)_windows_arm64.zip"
  | .architecture["arm64"].hash = $arm
' "$MANIFEST" > "$tmp"
mv "$tmp" "$MANIFEST"
echo "updated $MANIFEST to $BARE"

if command -v gh >/dev/null 2>&1; then
  gh release upload "$VERSION" "$MANIFEST" --clobber || echo "could not attach manifest to $VERSION"
fi

if [ -n "${SCOOP_BUCKET_TOKEN:-}" ]; then
  bucket="$(mktemp -d)"
  git clone --depth 1 \
    "https://x-access-token:${SCOOP_BUCKET_TOKEN}@github.com/daviddwlee84/scoop-bucket.git" "$bucket"
  mkdir -p "$bucket/bucket"
  cp "$MANIFEST" "$bucket/bucket/dev-cli.json"
  git -C "$bucket" add bucket/dev-cli.json
  if git -C "$bucket" diff --cached --quiet; then
    echo "bucket manifest already current"
  else
    git -C "$bucket" \
      -c user.name="dev-cli release" -c user.email="noreply@github.com" \
      commit -m "dev-cli ${BARE}"
    git -C "$bucket" push
    echo "pushed manifest to daviddwlee84/scoop-bucket"
  fi
else
  echo "SCOOP_BUCKET_TOKEN not set; manifest attached to the release only"
fi

exit 0
