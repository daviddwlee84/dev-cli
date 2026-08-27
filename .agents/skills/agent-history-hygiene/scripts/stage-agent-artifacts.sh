#!/usr/bin/env bash
# stage-agent-artifacts.sh — git-add agent chat + plan artifacts before
# the next commit.
#
# Default behavior: stage every dirty *.md under each directory in
# assets/artifact-dirs.txt, plus the current SpecStory transcript.
#
# Bash 3.2 compatible (stock macOS).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_DIRS_FILE="$SKILL_DIR/assets/artifact-dirs.txt"

usage() {
  cat <<'EOF'
Usage: stage-agent-artifacts.sh [OPTIONS]

Stage agent artifact files (SpecStory transcripts, .claude/plans/*.md, etc.)
for the next commit. Must be run from inside a git repo.

Options:
  --session-only        Only stage the current SpecStory transcript + newest
                        plan file; skip .cursor/, .opencode/, etc.
  --include-all-plans   Also re-add already-tracked plan/transcript files
                        that were modified (default: only unstaged files).
  --dirs-file PATH      Override the default artifact-dirs.txt location.
  --dry-run             Print the files that would be staged, but don't stage.
  --allow-empty         Stage artifacts even when HEAD has no code changes
                        (default: refuse — prevents "commit just transcript").
  --help, -h            Show this help and exit.

Exit codes:
  0  success (one or more artifacts staged or already clean)
  1  invalid arguments
  2  not inside a git repo
  3  no code changes AND no dirty artifacts (suggests nothing to commit)
  4  refused: code is clean but artifacts are dirty (use --allow-empty)
EOF
}

log()  { printf '%s\n' "$*" >&2; }
warn() { printf 'warn: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit "${2:-1}"; }

SESSION_ONLY=0
INCLUDE_ALL_PLANS=0
DIRS_FILE="$DEFAULT_DIRS_FILE"
DRY_RUN=0
ALLOW_EMPTY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --session-only)       SESSION_ONLY=1; shift ;;
    --include-all-plans)  INCLUDE_ALL_PLANS=1; shift ;;
    --dirs-file)          DIRS_FILE="${2:-}"; shift 2 ;;
    --dirs-file=*)        DIRS_FILE="${1#--dirs-file=}"; shift ;;
    --dry-run)            DRY_RUN=1; shift ;;
    --allow-empty)        ALLOW_EMPTY=1; shift ;;
    --help|-h)            usage; exit 0 ;;
    -*)                   die "unknown flag: $1 (try --help)" 1 ;;
    *)                    die "unexpected positional arg: $1 (try --help)" 1 ;;
  esac
done

# Must be in a git repo.
if ! git rev-parse --show-toplevel >/dev/null 2>&1; then
  die "not inside a git repo" 2
fi
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Read artifact dirs from the config file.
if [ ! -f "$DIRS_FILE" ]; then
  die "artifact-dirs.txt not found: $DIRS_FILE" 1
fi

ARTIFACT_DIRS=()
while IFS= read -r line; do
  # Strip comments and whitespace.
  line="${line%%#*}"
  line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [ -n "$line" ] && ARTIFACT_DIRS+=("$line")
done < "$DIRS_FILE"

# Warn if any artifact dir is silently gitignored.
check_ignored() {
  local dir="$1"
  # `git check-ignore` exits 0 if the path matches an ignore rule.
  if [ -e "$dir" ] && git check-ignore -q "$dir" 2>/dev/null; then
    warn "$dir is gitignored — staged files under it won't actually be committed"
  fi
}

# Resolve the "current session" artifacts for --session-only.
find_current_specstory() {
  "$SCRIPT_DIR/find-session.sh" --quiet --format=specstory 2>/dev/null \
    | awk -F'\t' '$1=="specstory_path"{print $2}'
}
find_current_plan() {
  [ -d ".claude/plans" ] || { printf ''; return 0; }
  find .claude/plans -maxdepth 1 -type f -name '*.md' -print0 2>/dev/null \
    | xargs -0 ls -t 2>/dev/null | head -n 1
}

# List dirty artifact files across the configured dirs.
# Returns null-separated paths on stdout.
list_dirty_artifacts() {
  local dir path git_status
  for dir in "${ARTIFACT_DIRS[@]}"; do
    [ -e "$dir" ] || continue
    check_ignored "$dir"
    # Include untracked (??) and modified (M/A) entries. `-z` → NUL separated.
    while IFS= read -r -d '' entry; do
      # porcelain -z format: XY<space>path[\0orig_path]
      # We only want the path portion.
      git_status="${entry:0:2}"
      path="${entry:3}"
      case "$path" in
        *.md) ;;
        *) continue ;;
      esac
      case "$git_status" in
        "??") printf '%s\0' "$path" ;;
        " M"|"MM"|"AM"|"AD"|"A ") printf '%s\0' "$path" ;;
        "M "|"A ")
          # Already staged — only re-add if --include-all-plans.
          [ "$INCLUDE_ALL_PLANS" = "1" ] && printf '%s\0' "$path"
          ;;
      esac
    done < <(git -c core.quotePath=false status --porcelain=v1 -z -uall -- "$dir" 2>/dev/null || true)
  done
}

# --- Main ---
STAGED_COUNT=0
CANDIDATES=()

if [ "$SESSION_ONLY" = "1" ]; then
  specstory=$(find_current_specstory)
  plan=$(find_current_plan)
  [ -n "$specstory" ] && [ -f "$specstory" ] && CANDIDATES+=("$specstory")
  [ -n "$plan" ] && [ -f "$plan" ] && CANDIDATES+=("$plan")
else
  # Capture the dirty artifact list.
  while IFS= read -r -d '' f; do
    CANDIDATES+=("$f")
  done < <(list_dirty_artifacts)
fi

# Check: are there code changes outside the artifact dirs?
# Non-artifact dirty files (staged or unstaged) mean the agent is committing
# a real feature alongside the transcript — that's the intended use.
has_code_changes() {
  local any=0 line path in_artifact dir
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    path="${line:3}"
    in_artifact=0
    for dir in "${ARTIFACT_DIRS[@]}"; do
      case "$path" in
        "$dir"/*) in_artifact=1; break ;;
      esac
    done
    [ "$in_artifact" = "0" ] && any=1
  done < <(git -c core.quotePath=false status --porcelain=v1 2>/dev/null)
  [ "$any" = "1" ]
}

if [ "${#CANDIDATES[@]}" -eq 0 ]; then
  if has_code_changes; then
    log "No agent artifacts to stage; code changes present — nothing to do."
    exit 0
  fi
  log "Nothing to stage: no code changes and no dirty artifacts."
  exit 3
fi

if [ "$ALLOW_EMPTY" = "0" ] && ! has_code_changes; then
  log "Refusing: artifacts are dirty but no code changes staged."
  log "         A transcript/plan commit without a feature diff is usually wrong."
  log "         Re-run with --allow-empty if you really want to commit artifacts alone."
  exit 4
fi

# Stage (or print).
for f in "${CANDIDATES[@]}"; do
  if [ "$DRY_RUN" = "1" ]; then
    printf '[dry-run] would git add: %s\n' "$f"
  else
    git add -- "$f"
    printf 'staged: %s\n' "$f"
  fi
  STAGED_COUNT=$((STAGED_COUNT + 1))
done

log "${STAGED_COUNT} artifact(s) ${DRY_RUN:+would be }staged."
