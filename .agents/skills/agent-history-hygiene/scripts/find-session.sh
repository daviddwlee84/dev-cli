#!/usr/bin/env bash
# find-session.sh — resolve the current agent session files for $PWD.
#
# Returns the SpecStory transcript path + Claude Code session UUID/JSONL
# path for the current working directory, so the calling agent can stage
# the right artifacts before a commit.
#
# Bash 3.2 compatible (stock macOS).

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: find-session.sh [OPTIONS]

Discover the agent session files associated with $PWD. Heuristics:
  - SpecStory: newest *.md under ./.specstory/history/
  - Claude Code: newest *.jsonl under ~/.claude/projects/<slug>/ where
                 <slug> is $PWD with '/' -> '-' (Claude's own convention).

Options:
  --format=VALUE     one of: specstory, claude, both (default: both)
  --json             Emit JSON instead of TSV.
  --quiet            Suppress stderr diagnostics on empty results.
  --help, -h         Show this help and exit.

Output (TSV, one key/value per line):
  specstory_path       <abs path or empty>
  claude_session_uuid  <uuid or empty>
  claude_jsonl_path    <abs path or empty>

Exit codes:
  0  always (never fails the pipeline; empty values signal absence).
  1  invalid arguments.
EOF
}

log()  { [ "$QUIET" = "1" ] || printf '%s\n' "$*" >&2; }
warn() { printf 'warn: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit "${2:-1}"; }

FORMAT="both"
OUT_JSON=0
QUIET=0

while [ $# -gt 0 ]; do
  case "$1" in
    --format=*) FORMAT="${1#--format=}"; shift ;;
    --format)   FORMAT="${2:-both}"; shift 2 ;;
    --json)     OUT_JSON=1; shift ;;
    --quiet)    QUIET=1; shift ;;
    --help|-h)  usage; exit 0 ;;
    -*)         die "unknown flag: $1 (try --help)" 1 ;;
    *)          die "unexpected positional arg: $1 (try --help)" 1 ;;
  esac
done

case "$FORMAT" in
  both|specstory|claude) ;;
  *) die "invalid --format: $FORMAT (expected both|specstory|claude)" 1 ;;
esac

# Claude's project slug: absolute $PWD with every NON-ALPHANUMERIC character
# replaced by '-' -- not just '/'.
#   /Volumes/Data/x          -> -Volumes-Data-x
#   /home/u/.local/share/foo -> -home-u--local-share-foo   (the dot collapses)
# The leading dash is intentional -- it mirrors how Claude Code names the
# subdirectory under ~/.claude/projects/. Using `tr '/' '-'` here silently
# returns an empty result for any path containing '.', '_' or a space.
cwd_slug() {
  printf '%s' "$PWD" | sed 's|[^A-Za-z0-9]|-|g'
}

newest_file() {
  # newest_file <dir> <glob>
  # Prints the newest matching file by mtime, or empty if none.
  local dir="$1" glob="$2"
  [ -d "$dir" ] || { printf ''; return 0; }
  # stat -f on macOS, stat -c on Linux — use find+sort for portability.
  local found
  found=$(find "$dir" -maxdepth 1 -type f -name "$glob" -print0 2>/dev/null \
    | xargs -0 ls -t 2>/dev/null \
    | head -n 1)
  printf '%s' "$found"
}

SPECSTORY_PATH=""
CLAUDE_JSONL=""
CLAUDE_UUID=""

if [ "$FORMAT" = "both" ] || [ "$FORMAT" = "specstory" ]; then
  candidate=$(newest_file "$PWD/.specstory/history" "*.md")
  if [ -n "$candidate" ]; then
    SPECSTORY_PATH="$candidate"
  else
    log "no .specstory/history/*.md under $PWD"
  fi
fi

if [ "$FORMAT" = "both" ] || [ "$FORMAT" = "claude" ]; then
  slug=$(cwd_slug)
  proj_dir="$HOME/.claude/projects/$slug"
  candidate=$(newest_file "$proj_dir" "*.jsonl")
  if [ -n "$candidate" ]; then
    CLAUDE_JSONL="$candidate"
    CLAUDE_UUID=$(basename "$candidate" .jsonl)
  else
    log "no ~/.claude/projects/$slug/*.jsonl (slug derived from PWD)"
  fi
fi

if [ "$OUT_JSON" = "1" ]; then
  # Hand-rolled JSON keeps us dependency-free (no jq/python requirement).
  printf '{"specstory_path":"%s","claude_session_uuid":"%s","claude_jsonl_path":"%s"}\n' \
    "$SPECSTORY_PATH" "$CLAUDE_UUID" "$CLAUDE_JSONL"
else
  printf 'specstory_path\t%s\n' "$SPECSTORY_PATH"
  printf 'claude_session_uuid\t%s\n' "$CLAUDE_UUID"
  printf 'claude_jsonl_path\t%s\n' "$CLAUDE_JSONL"
fi
