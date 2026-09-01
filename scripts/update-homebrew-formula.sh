#!/usr/bin/env bash
# Render the Homebrew formula for one stable release and publish it to the tap.
# The release workflow supplies HOMEBREW_TAP_TOKEN, a fine-grained token with
# Contents: write permission for daviddwlee84/homebrew-tap.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: update-homebrew-formula.sh [--output PATH] [--source-sha256 HEX] vX.Y.Z

Without --output, update Formula/dev-cli.rb in daviddwlee84/homebrew-tap.
HOMEBREW_TAP_TOKEN is required for publishing. --output renders locally and
does not contact the tap; --source-sha256 avoids downloading the tag archive.
EOF
}

OUTPUT=""
SOURCE_SHA256=""
VERSION=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      OUTPUT="$2"
      shift 2
      ;;
    --source-sha256)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      SOURCE_SHA256="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    -*)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      if [ -n "$VERSION" ]; then
        echo "unexpected argument: $1" >&2
        usage >&2
        exit 2
      fi
      VERSION="$1"
      shift
      ;;
  esac
done

if [[ ! "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "version must be a stable vMAJOR.MINOR.PATCH tag" >&2
  exit 2
fi
if [ -z "$OUTPUT" ] && [ -z "${HOMEBREW_TAP_TOKEN:-}" ]; then
  echo "HOMEBREW_TAP_TOKEN is required to update daviddwlee84/homebrew-tap" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$ROOT/packaging/homebrew/dev-cli.rb.tmpl"
BARE="${VERSION#v}"
ARCHIVE_URL="https://github.com/daviddwlee84/dev-cli/archive/refs/tags/${VERSION}.tar.gz"
TMPDIR_FORMULA="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_FORMULA"' EXIT

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

if [ -z "$SOURCE_SHA256" ]; then
  archive="$TMPDIR_FORMULA/source.tar.gz"
  curl --fail --location --retry 3 --silent --show-error "$ARCHIVE_URL" --output "$archive"
  SOURCE_SHA256="$(hash_file "$archive")"
fi
if [[ ! "$SOURCE_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "source SHA256 must be exactly 64 hexadecimal characters" >&2
  exit 2
fi
SOURCE_SHA256="$(printf '%s' "$SOURCE_SHA256" | tr '[:upper:]' '[:lower:]')"

rendered="$TMPDIR_FORMULA/dev-cli.rb"
sed \
  -e "s/@VERSION@/$BARE/g" \
  -e "s/@SHA256@/$SOURCE_SHA256/g" \
  "$TEMPLATE" > "$rendered"
if grep -Eq '@(VERSION|SHA256)@' "$rendered"; then
  echo "formula template still contains an unresolved placeholder" >&2
  exit 1
fi

if [ -n "$OUTPUT" ]; then
  if [ "$OUTPUT" = "-" ]; then
    command cat "$rendered"
  else
    cp "$rendered" "$OUTPUT"
    echo "rendered Homebrew formula for $VERSION at $OUTPUT"
  fi
  exit 0
fi

for command_name in gh jq base64 git; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "$command_name is required to publish the Homebrew formula" >&2
    exit 1
  }
done

api_path="repos/daviddwlee84/homebrew-tap/contents/Formula/dev-cli.rb"
metadata="$(GH_TOKEN="$HOMEBREW_TAP_TOKEN" gh api "$api_path" 2>/dev/null || true)"
remote_sha="$(jq -r '.sha // empty' <<<"$metadata")"
local_sha="$(git hash-object "$rendered")"
if [ "$remote_sha" = "$local_sha" ]; then
  echo "Homebrew formula is already current for $VERSION"
  exit 0
fi

content="$(base64 < "$rendered" | tr -d '\n')"
if [ -n "$remote_sha" ]; then
  payload="$(jq -n \
    --arg message "dev-cli $BARE" \
    --arg content "$content" \
    --arg sha "$remote_sha" \
    '{message: $message, content: $content, sha: $sha}')"
else
  payload="$(jq -n \
    --arg message "dev-cli $BARE" \
    --arg content "$content" \
    '{message: $message, content: $content}')"
fi

GH_TOKEN="$HOMEBREW_TAP_TOKEN" gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "$api_path" \
  --input - <<<"$payload" >/dev/null
echo "published dev-cli $BARE to daviddwlee84/homebrew-tap"
