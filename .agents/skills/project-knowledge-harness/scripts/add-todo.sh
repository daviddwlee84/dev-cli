#!/usr/bin/env bash
set -euo pipefail

# Insert a structured entry into TODO.md under the matching `## P*` lane.
# Optionally scaffold backlog/<slug>.md from the backlog-doc template.
#
# Compatibility: macOS system Bash 3.2 (no associative arrays, no readarray).

usage() {
  cat <<'EOF'
Usage: add-todo.sh [OPTIONS] --priority {P1|P2|P3|P?} --effort {S|M|L|XL|?} \
                             --title TEXT --description TEXT

Insert a canonically-formatted entry into TODO.md under the matching ## P*
lane. The validator (todo-kanban.sh) is re-run after the edit; if validation
fails, the original TODO.md is restored.

Required:
  --priority {P1|P2|P3|P?}    Lane to insert into.
  --effort {S|M|L|XL|?}       Effort tag. `?` is only valid with `P?`.
  --title TEXT                Item title. Must not contain `*`.
  --description TEXT          Item description (free-form after the em-dash).

Options:
  --backlog                   Also scaffold backlog/<slug>.md from the
                              project-knowledge-harness backlog template
                              and append ` → [research](backlog/<slug>.md)`
                              to the new TODO line.
  --slug SLUG                 Override the auto-derived backlog slug.
  --file PATH                 TODO file (default: TODO.md).
  --backlog-dir DIR           Where to write the backlog doc (default: backlog).
  --template PATH             Path to backlog-doc.md.template
                              (default: looked up next to this script then
                              under skills/local/project-knowledge-harness/assets/).
  --validator PATH            Path to todo-kanban.sh
                              (default: sibling script).
  --dry-run                   Print the rewritten TODO.md to stdout and the
                              would-be backlog doc, do not modify files.
  -h, --help                  Show this help and exit.

Exit codes:
  0  TODO entry written and validation passed.
  1  Bad input / file not found / validation failure / template missing.
  2  Usage error.
EOF
}

priority=""
effort=""
title=""
description=""
file="TODO.md"
backlog=0
slug_override=""
backlog_dir="backlog"
template=""
validator=""
dry_run=0

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --priority) priority="$2"; shift 2 ;;
    --effort) effort="$2"; shift 2 ;;
    --title) title="$2"; shift 2 ;;
    --description) description="$2"; shift 2 ;;
    --backlog) backlog=1; shift ;;
    --slug) slug_override="$2"; shift 2 ;;
    --file) file="$2"; shift 2 ;;
    --backlog-dir) backlog_dir="$2"; shift 2 ;;
    --template) template="$2"; shift 2 ;;
    --validator) validator="$2"; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$priority" ] || [ -z "$effort" ] || [ -z "$title" ] || [ -z "$description" ]; then
  echo "Error: --priority, --effort, --title, and --description are all required" >&2
  usage >&2
  exit 2
fi

case "$priority" in
  P1|P2|P3|"P?") ;;
  *) echo "Error: --priority must be one of P1, P2, P3, P?" >&2; exit 2 ;;
esac

case "$effort" in
  S|M|L|XL) ;;
  "?")
    if [ "$priority" != "P?" ]; then
      echo "Error: --effort '?' is only valid with --priority 'P?'" >&2
      exit 2
    fi
    ;;
  *) echo "Error: --effort must be one of S, M, L, XL (or '?' with P?)" >&2; exit 2 ;;
esac

case "$title" in
  *"*"*)
    echo "Error: --title must not contain '*' (breaks the validator's bold delimiters)" >&2
    exit 2
    ;;
esac

if [ ! -f "$file" ]; then
  echo "Error: file not found: $file" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

# Locate the validator (sibling, then top-level scripts/, then inside the skill).
if [ -z "$validator" ]; then
  for cand in \
    "$script_dir/todo-kanban.sh" \
    "$repo_root/scripts/todo-kanban.sh" \
    "$repo_root/skills/local/project-knowledge-harness/scripts/todo-kanban.sh"
  do
    if [ -x "$cand" ]; then
      validator="$cand"
      break
    fi
  done
fi

# Locate the backlog template if needed.
if [ "$backlog" -eq 1 ] && [ -z "$template" ]; then
  for cand in \
    "$script_dir/../assets/backlog-doc.md.template" \
    "$repo_root/skills/local/project-knowledge-harness/assets/backlog-doc.md.template"
  do
    if [ -f "$cand" ]; then
      template="$cand"
      break
    fi
  done
fi

if [ "$backlog" -eq 1 ] && [ -z "$template" ]; then
  echo "Error: --backlog set but backlog-doc template not found; pass --template explicitly" >&2
  exit 1
fi

# Derive slug from title if not overridden.
derive_slug() {
  local s="$1"
  # Lowercase
  s="$(printf '%s' "$s" | tr '[:upper:]' '[:lower:]')"
  # Replace anything that's not [a-z0-9] with `-`
  s="$(printf '%s' "$s" | LC_ALL=C sed 's/[^a-z0-9]/-/g')"
  # Collapse multiple `-`
  while case "$s" in *--*) true;; *) false;; esac; do
    s="$(printf '%s' "$s" | sed 's/--/-/g')"
  done
  # Strip leading/trailing `-`
  s="${s#-}"
  s="${s%-}"
  printf '%s' "$s"
}

slug=""
if [ "$backlog" -eq 1 ]; then
  if [ -n "$slug_override" ]; then
    slug="$slug_override"
  else
    slug="$(derive_slug "$title")"
  fi
  if [ -z "$slug" ]; then
    echo "Error: could not derive a slug from --title; pass --slug explicitly" >&2
    exit 1
  fi
fi

# Build the new TODO line.
case "$priority" in
  "P?")
    new_line="- [ ] **[?/${effort}] ${title}** — ${description}"
    ;;
  *)
    new_line="- [ ] **[${effort}] ${title}** — ${description}"
    ;;
esac

if [ "$backlog" -eq 1 ]; then
  new_line="${new_line} → [research](${backlog_dir}/${slug}.md)"
fi

# Insert the new line at the END of the matching ## P* section.
# "End" = right before the next `## ` heading (or EOF).
target_heading="## ${priority}"

tmp="$(mktemp -t add-todo.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

awk -v target="$target_heading" -v new_line="$new_line" '
  BEGIN { in_target = 0; inserted = 0; pending_blanks = 0 }
  {
    line = $0
    if (line == target) {
      if (in_target && !inserted) {
        # Defensive: shouldn'\''t happen (only one matching heading), but flush.
        for (i = 0; i < pending_blanks; i++) print ""
        pending_blanks = 0
        print new_line
        inserted = 1
      }
      in_target = 1
      print line
      next
    }
    # Any new `## ` heading ends the target section.
    if (in_target && substr(line, 1, 3) == "## ") {
      if (!inserted) {
        print new_line
        for (i = 0; i < pending_blanks; i++) print ""
        pending_blanks = 0
        inserted = 1
      }
      in_target = 0
      print line
      next
    }
    if (in_target) {
      # Buffer trailing blank lines so the new entry sits flush with the
      # last non-blank line in the section, with one blank before the next
      # heading.
      if (line == "") {
        pending_blanks++
      } else {
        for (i = 0; i < pending_blanks; i++) print ""
        pending_blanks = 0
        print line
      }
    } else {
      print line
    }
  }
  END {
    if (in_target && !inserted) {
      for (i = 0; i < pending_blanks; i++) print ""
      print new_line
      inserted = 1
    }
    if (!inserted) {
      # Emit a sentinel so the caller can detect failure.
      exit 3
    }
  }
' "$file" > "$tmp" || {
  rc=$?
  if [ "$rc" -eq 3 ]; then
    echo "Error: could not find heading '$target_heading' in $file" >&2
    exit 1
  fi
  exit "$rc"
}

# If --backlog and the destination doc already exists, refuse.
backlog_path=""
if [ "$backlog" -eq 1 ]; then
  backlog_path="${backlog_dir}/${slug}.md"
  if [ -e "$backlog_path" ]; then
    echo "Error: backlog doc already exists: $backlog_path (use --slug to override)" >&2
    exit 1
  fi
fi

if [ "$dry_run" -eq 1 ]; then
  printf '# Proposed TODO.md\n\n'
  cat "$tmp"
  if [ "$backlog" -eq 1 ]; then
    printf '\n# Proposed %s\n\n' "$backlog_path"
    sed "s/<Topic title>/${title//\//\\/}/" "$template"
  fi
  exit 0
fi

# Write the new TODO file with backup.
backup="$file.add-todo-bak.$$"
cp "$file" "$backup"
mv "$tmp" "$file"
trap - EXIT

# Validate.
if [ -n "$validator" ] && [ -x "$validator" ]; then
  if ! "$validator" --validate-only "$file" >/dev/null 2>&1; then
    echo "Error: validation failed after edit; restoring original $file" >&2
    "$validator" --validate-only "$file" >&2 || true
    mv "$backup" "$file"
    exit 1
  fi
fi
rm -f "$backup"

# Scaffold backlog doc if requested.
if [ "$backlog" -eq 1 ]; then
  mkdir -p "$backlog_dir"
  # Substitute the title into the H1 placeholder; leave other sections alone.
  sed "s/<Topic title>/${title//\//\\/}/" "$template" > "$backlog_path"
  echo "Created backlog doc: $backlog_path" >&2
fi

echo "Added to ## $priority: $new_line" >&2
