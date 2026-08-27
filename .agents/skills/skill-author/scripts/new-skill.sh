#!/usr/bin/env bash
# new-skill.sh — Scaffold a new agent skill in the npx-skills-compatible layout.
#
# Three placement scopes (auto-detected by default, override with --local /
# --project / --global):
#
#   LOCAL    Publishing-repo workflow (this very repo). Canonical content lives
#            under <repo>/skills/local/<name>/ (or skills/vendor/<name>/ with
#            --vendor), and discovery symlinks are added at
#            <repo>/.agents/skills/<name> and <repo>/.claude/skills/<name>
#            -> ../../skills/{local,vendor}/<name>.
#
#   PROJECT  Generic per-project install. Canonical content at
#            <repo>/.agents/skills/<name>/ (universal agents like Cursor /
#            Codex / OpenCode / Warp pick this up directly), plus a relative
#            symlink at <repo>/.claude/skills/<name> -> ../../.agents/skills/<name>
#            (and other already-present non-universal agent dirs).
#
#   GLOBAL   System-wide install. Canonical content at ~/.agents/skills/<name>/
#            plus a relative symlink at ~/.claude/skills/<name> ->
#            ../../.agents/skills/<name> (and any other detected agent dir).
#
# Auto-detection precedence (when no scope flag given):
#   1. publishing repo (vendor.yaml, skills/local/, or
#      skills/.claude-plugin/marketplace.json found walking up) -> LOCAL
#   2. inside a git repo (.git found walking up)                 -> PROJECT
#   3. otherwise                                                 -> GLOBAL
#
# Bash 3.2 compatible (works on stock macOS — no mapfile, no ${var,,},
# no [[ -v var ]]).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ASSETS_DIR="$SKILL_DIR/assets"

usage() {
  cat <<'EOF'
Usage: new-skill.sh [OPTIONS] <skill-name>

Scaffold a new agent skill with the standard layout (SKILL.md, references/,
scripts/, assets/) seeded from skill-author's templates, then place it where
the active scope says it belongs and add discovery symlinks for non-universal
agents.

Scope (mutually exclusive — auto-detected if omitted):
  --local            Force LOCAL mode (this publishing repo's skills/local/).
                     Requires a vendor.yaml / skills/local/ / skills/.claude-plugin
                     anchor walking up from --root or CWD.
  --project          Force PROJECT mode (<repo>/.agents/skills/<name>/).
                     Requires a git repo root (.git) walking up.
  --global           Force GLOBAL mode (~/.agents/skills/<name>/).

Other options:
  --vendor           In LOCAL mode, place under skills/vendor/<name>/ instead of
                     skills/local/. (Rare — vendored skills normally come via
                     vendor.yaml + scripts/sync-vendor.sh.)
  --root DIR         Override repo / project root discovery.
  --no-symlinks      Skip the agent-dir discovery symlinks (just write the
                     canonical dir). Useful for CI / packaging tests.
  --dry-run          Print what would be created without writing.
  --force            Overwrite if the target canonical directory already exists.
                     Existing symlinks are always replaced.
  --help, -h         Show this help and exit.

Examples:
  bash new-skill.sh my-skill                # auto-detect scope
  bash new-skill.sh --project my-skill      # force project (.agents/skills)
  bash new-skill.sh --global my-skill       # force user-wide (~/.agents/skills)
  bash new-skill.sh --local my-skill        # force skills/local/ (publish repo)
  bash new-skill.sh --dry-run my-skill      # see what would happen
  bash new-skill.sh --root /path/to/repo my-skill

Output:
  Single JSON object on stdout (parseable by an agent), prose on stderr.
  Keys: skill, mode, canonical, symlinks[], next_steps[].

Exit codes:
  0  success
  1  invalid arguments
  2  target canonical directory already exists (use --force to overwrite)
  3  scope precondition failed (e.g. --local outside a publishing repo,
     --project outside a git repo, template missing)
  4  symlink creation produced a broken link (post-write verification failed)
EOF
}

log()  { printf '%s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$1" >&2; exit "${2:-1}"; }

# ----------------------------------------------------------------------
# Arg parsing
# ----------------------------------------------------------------------
NAME=""
ROOT_OVERRIDE=""
DRY_RUN=0
FORCE=0
VENDOR=0
NO_SYMLINKS=0
MODE=""  # "", "local", "project", "global"

set_mode() {
  if [ -n "$MODE" ] && [ "$MODE" != "$1" ]; then
    die "conflicting scope flags: already set to --$MODE, got --$1" 1
  fi
  MODE="$1"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --local)        set_mode local;   shift ;;
    --project)      set_mode project; shift ;;
    --global)       set_mode global;  shift ;;
    --vendor)       VENDOR=1; shift ;;
    --root)         ROOT_OVERRIDE="${2:-}"; shift 2 ;;
    --no-symlinks)  NO_SYMLINKS=1; shift ;;
    --dry-run)      DRY_RUN=1; shift ;;
    --force)        FORCE=1; shift ;;
    --help|-h)      usage; exit 0 ;;
    -*)             die "unknown flag: $1 (try --help)" 1 ;;
    *)
      if [ -n "$NAME" ]; then
        die "only one skill name allowed (got '$NAME' and '$1')" 1
      fi
      NAME="$1"; shift
      ;;
  esac
done

[ -n "$NAME" ] || die "missing <skill-name> (try --help)" 1

# Validate name (kebab-case, no spaces, no leading dots).
case "$NAME" in
  -*|.*)            die "invalid skill name: '$NAME' (cannot start with '-' or '.')" 1 ;;
  *[!a-zA-Z0-9_-]*) die "invalid skill name: '$NAME' (use a-z, 0-9, _, -)" 1 ;;
esac

if [ "$VENDOR" = "1" ] && [ -n "$MODE" ] && [ "$MODE" != "local" ]; then
  die "--vendor only makes sense in --local mode (publishing-repo workflow)" 1
fi

# ----------------------------------------------------------------------
# Scope discovery
# ----------------------------------------------------------------------

# Walk up from $1 looking for a directory matching the predicate (a callable
# that takes the candidate dir and returns 0 if it matches).
walk_up_for() {
  local start="$1" predicate="$2"
  local cur="$start"
  while [ "$cur" != "/" ] && [ -n "$cur" ]; do
    if "$predicate" "$cur"; then
      printf '%s\n' "$cur"
      return 0
    fi
    cur="$(dirname "$cur")"
  done
  return 1
}

# Publishing repo marker: vendor.yaml at root, OR skills/local/, OR
# skills/.claude-plugin/marketplace.json.
is_publishing_anchor() {
  [ -f "$1/vendor.yaml" ] || \
  [ -d "$1/skills/local" ] || \
  [ -f "$1/skills/.claude-plugin/marketplace.json" ]
}

is_git_root() {
  [ -d "$1/.git" ] || [ -f "$1/.git" ]
}

START_DIR="${ROOT_OVERRIDE:-$(pwd)}"
[ -d "$START_DIR" ] || die "start dir does not exist: $START_DIR" 1

PUB_ROOT=""
PUB_ROOT="$(walk_up_for "$START_DIR" is_publishing_anchor 2>/dev/null || true)"
GIT_ROOT=""
GIT_ROOT="$(walk_up_for "$START_DIR" is_git_root 2>/dev/null || true)"

if [ -z "$MODE" ]; then
  if [ -n "$PUB_ROOT" ]; then
    MODE="local"
  elif [ -n "$GIT_ROOT" ]; then
    MODE="project"
  else
    MODE="global"
  fi
fi

# Validate scope preconditions and pick base + canonical dirs.
BASE_DIR=""
CANONICAL_PARENT=""
CANONICAL_DIR=""
SUBDIR_LABEL=""

case "$MODE" in
  local)
    if [ -z "$PUB_ROOT" ]; then
      die "--local requires a publishing-repo anchor (vendor.yaml, skills/local/, or skills/.claude-plugin/marketplace.json) walking up from $START_DIR" 3
    fi
    BASE_DIR="$PUB_ROOT"
    if [ "$VENDOR" = "1" ]; then
      SUBDIR_LABEL="vendor"
      CANONICAL_PARENT="$BASE_DIR/skills/vendor"
    else
      SUBDIR_LABEL="local"
      CANONICAL_PARENT="$BASE_DIR/skills/local"
    fi
    CANONICAL_DIR="$CANONICAL_PARENT/$NAME"
    ;;
  project)
    if [ -z "$GIT_ROOT" ]; then
      die "--project requires a git repo (.git found walking up from $START_DIR). Use --global for ad-hoc skills outside any repo." 3
    fi
    BASE_DIR="$GIT_ROOT"
    SUBDIR_LABEL="agents"
    CANONICAL_PARENT="$BASE_DIR/.agents/skills"
    CANONICAL_DIR="$CANONICAL_PARENT/$NAME"
    ;;
  global)
    BASE_DIR="$HOME"
    SUBDIR_LABEL="agents"
    CANONICAL_PARENT="$BASE_DIR/.agents/skills"
    CANONICAL_DIR="$CANONICAL_PARENT/$NAME"
    ;;
  *) die "internal: unknown mode '$MODE'" 1 ;;
esac

if [ -e "$CANONICAL_DIR" ] && [ "$FORCE" = "0" ]; then
  die "target already exists: $CANONICAL_DIR (use --force to overwrite)" 2
fi

# ----------------------------------------------------------------------
# Non-universal agent fan-out table
# ----------------------------------------------------------------------
#
# Universal agents (cursor, codex, opencode, gemini-cli, github-copilot, warp,
# zed, amp, cline, antigravity, deepagents, dexto, firebender, kimi-code-cli,
# loaf, replit, universal) read .agents/skills directly and need NO symlink.
#
# This list mirrors the non-universal subset of vercel-labs/skills's
# src/agents.ts that we realistically need to fan out to. Format:
#
#   "<config-root>|<skills-subdir>"
#
# We always fan out to claude-code; for everything else, we only create the
# symlink if its config root directory ALREADY exists at the base dir (mirrors
# upstream's "don't create .windsurf/ unless it's actually used in this
# project" behavior). The leading config-root check is what makes this safe to
# call on a fresh machine.

NONUNIVERSAL_AGENTS_IF_PRESENT="\
.windsurf|skills
.kilocode|skills
.kiro|skills
.junie|skills
.roo|skills
.aider-desk|skills
.augment|skills
.continue|skills
.cortex|skills
.crush|skills
.qoder|skills
.qwen|skills
.factory|skills
.codebuddy|skills
.openhands|skills
.trae|skills
.codemaker|skills
.codestudio|skills
.commandcode|skills
.codeartsdoer|skills
.zencoder|skills
.tabnine/agent|skills
.iflow|skills
.kode|skills
.pi/agent|skills
.lingma|skills
.mcpjam|skills
.adal|skills
.bob|skills
.forge|skills
.snowflake/cortex|skills
.rovodev|skills
.ona|skills
.mux|skills
.moxby|skills
.devin|skills
.pochi|skills
.neovate|skills
.terramind|skills
.tinycloud|skills
.reasonix|skills
.jazz|skills
.inferencesh|skills
.hermes|skills
.autohand|skills
.vibe|skills"

# ----------------------------------------------------------------------
# Helpers
# ----------------------------------------------------------------------

create_dir() {
  if [ "$DRY_RUN" = "1" ]; then
    log "[dry-run] mkdir -p $1"
  else
    mkdir -p "$1"
  fi
}

write_template() {
  local src="$1" dst="$2"
  if [ "$DRY_RUN" = "1" ]; then
    log "[dry-run] cp $src -> $dst"
    return 0
  fi
  if [ ! -f "$src" ]; then
    die "template missing: $src" 3
  fi
  cp "$src" "$dst"
}

substitute() {
  local file="$1"
  if [ "$DRY_RUN" = "1" ]; then
    log "[dry-run] would substitute placeholders in $file"
    return 0
  fi
  # Bash 3.2 safe: sed -i with a backup, then drop the backup file (the BSD
  # vs GNU sed split is the reason we go through a .bak detour).
  sed -i.bak \
    -e "s/SKILL_NAME_PLACEHOLDER/$NAME/g" \
    -e "s/SKILL_TITLE_PLACEHOLDER/$(printf '%s' "$NAME" | tr '-' ' ')/g" \
    "$file"
  rm -f "${file}.bak"
}

# Symlink at <linkdir>/<NAME> -> ../../<rel-target>, then verify the link is
# resolvable (test -e). All three placement scopes use the same fixed two-
# levels-up form because both <linkdir> and <canonical-parent> are exactly
# two levels under the base dir (.claude/skills/, .agents/skills/, etc.) —
# see pitfalls/symlink-target-relative-to-symlink-not-cwd.md.
#
# Args: <link-skills-dir> <relative-target-suffix>
# Example: link_into "$BASE_DIR/.claude/skills" "skills/local"
#          -> creates .claude/skills/<NAME> -> ../../skills/local/<NAME>
link_into() {
  local link_skills_dir="$1" target_suffix="$2"
  local link_path="$link_skills_dir/$NAME"
  local relative_target="../../$target_suffix/$NAME"

  if [ "$DRY_RUN" = "1" ]; then
    log "[dry-run] mkdir -p $link_skills_dir && ln -snf $relative_target $link_path"
    SYMLINKS_CREATED="$SYMLINKS_CREATED
$link_path -> $relative_target"
    return 0
  fi

  mkdir -p "$link_skills_dir"

  # Replace any existing entry (symlink, broken symlink, dir, or file). We do
  # NOT recursively delete real content here on purpose — if the path is a
  # real directory, the user pointed --force at the wrong place.
  if [ -L "$link_path" ]; then
    rm -f "$link_path"
  elif [ -e "$link_path" ]; then
    if [ "$FORCE" = "1" ]; then
      rm -rf "$link_path"
    else
      die "would clobber non-symlink at $link_path (use --force to replace)" 2
    fi
  fi

  ln -s "$relative_target" "$link_path"

  if [ ! -e "$link_path/SKILL.md" ]; then
    die "created symlink is dangling: $link_path -> $relative_target (resolved from $link_skills_dir). This is the pitfall described in pitfalls/symlink-target-relative-to-symlink-not-cwd.md." 4
  fi

  SYMLINKS_CREATED="$SYMLINKS_CREATED
$link_path -> $relative_target"
}

# Resolve the target suffix that all symlinks for the current mode share.
# - LOCAL:   skills/local  (or skills/vendor with --vendor)
# - PROJECT: .agents/skills
# - GLOBAL:  .agents/skills
target_suffix_for_mode() {
  case "$MODE" in
    local)   [ "$VENDOR" = "1" ] && printf 'skills/vendor\n' || printf 'skills/local\n' ;;
    project) printf '.agents/skills\n' ;;
    global)  printf '.agents/skills\n' ;;
  esac
}

# ----------------------------------------------------------------------
# Scaffold the canonical directory
# ----------------------------------------------------------------------
log "Mode:      $MODE"
log "Base:      $BASE_DIR"
log "Canonical: $CANONICAL_DIR"

create_dir "$CANONICAL_DIR"
create_dir "$CANONICAL_DIR/references"
create_dir "$CANONICAL_DIR/scripts"
create_dir "$CANONICAL_DIR/assets"

write_template "$ASSETS_DIR/SKILL.md.template" "$CANONICAL_DIR/SKILL.md"
substitute "$CANONICAL_DIR/SKILL.md"

# .gitkeep so empty subdirs survive `git add`.
if [ "$DRY_RUN" = "0" ]; then
  for sub in references scripts assets; do
    if [ -z "$(ls -A "$CANONICAL_DIR/$sub" 2>/dev/null)" ]; then
      : > "$CANONICAL_DIR/$sub/.gitkeep"
    fi
  done
fi

# ----------------------------------------------------------------------
# Fan-out symlinks
# ----------------------------------------------------------------------
SYMLINKS_CREATED=""
TARGET_SUFFIX="$(target_suffix_for_mode)"

if [ "$NO_SYMLINKS" = "1" ]; then
  log "Symlinks:  skipped (--no-symlinks)"
else
  if [ "$MODE" = "local" ]; then
    # In LOCAL mode also add the .agents/skills convenience link in addition to
    # .claude/skills, because this repo dogfoods .agents/skills as a discovery
    # farm even though it isn't a "consumer install".
    link_into "$BASE_DIR/.agents/skills" "$TARGET_SUFFIX"
  fi

  # claude-code: always fanned out (most-used non-universal agent).
  link_into "$BASE_DIR/.claude/skills" "$TARGET_SUFFIX"

  # Other non-universal agents: only if their config root already exists at
  # $BASE_DIR (mirrors upstream's "don't create .windsurf/ unless present"
  # rule). Use a heredoc rather than a pipe so the while-loop runs in the
  # parent shell — preserves SYMLINKS_CREATED mutations and lets `die` exit
  # the script.
  while IFS='|' read -r root_dir sub; do
    [ -n "$root_dir" ] || continue
    case "$root_dir" in \#*) continue ;; esac
    if [ -d "$BASE_DIR/$root_dir" ]; then
      link_into "$BASE_DIR/$root_dir/$sub" "$TARGET_SUFFIX"
    fi
  done <<EOF
$NONUNIVERSAL_AGENTS_IF_PRESENT
EOF
fi

# ----------------------------------------------------------------------
# Structured success output
# ----------------------------------------------------------------------
if [ "$DRY_RUN" = "1" ]; then
  log "Dry run complete. Re-run without --dry-run to actually create files."
  exit 0
fi

# Build a JSON array of symlinks for the structured output. bash 3.2 safe.
json_symlinks() {
  printf '['
  local first=1 line
  # Read from a temp string; the while-loop body runs in the parent shell
  # because we use here-string, so we can mutate `first`.
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    if [ "$first" = "1" ]; then first=0; else printf ','; fi
    # line is "<link> -> <target>" — keep that as a single string.
    printf '"%s"' "$(printf '%s' "$line" | sed 's/"/\\"/g')"
  done <<EOF
$SYMLINKS_CREATED
EOF
  printf ']'
}

NEXT_STEPS="\"Edit $CANONICAL_DIR/SKILL.md to fill in description and workflow\",\"Run lint-skill.sh $CANONICAL_DIR to verify\""

printf '{"skill":"%s","mode":"%s","canonical":"%s","symlinks":%s,"next_steps":[%s]}\n' \
  "$NAME" "$MODE" "$CANONICAL_DIR" "$(json_symlinks)" "$NEXT_STEPS"
