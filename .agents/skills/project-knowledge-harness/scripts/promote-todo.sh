#!/usr/bin/env bash
set -euo pipefail

# Move a TODO.md item from its active lane to `## Done` with the dated
# project-knowledge-harness "Done" syntax. Re-runs the validator after
# editing so syntax drift is caught immediately.
#
# Compatibility: macOS system Bash 3.2.

usage() {
  cat <<'EOF'
Usage: promote-todo.sh [OPTIONS] --title <SUBSTRING> --summary <SHIPPED SUMMARY>

Move an active TODO item to the `## Done` section, rewriting it as:
  - ✅ [YYYY-MM-DD] [P#/Effort] Title — <summary>

The script edits TODO.md in-place. If validation fails after the edit,
the original file is restored.

Required:
  --title SUBSTRING     Match an active item whose Title contains SUBSTRING
                        (case-sensitive). Must match exactly one active item.
  --summary TEXT        One-line summary of what shipped.

Options:
  --file FILE           TODO file (default: TODO.md)
  --date YYYY-MM-DD     Override completion date (default: today, UTC)
  --dry-run             Print the new file to stdout, do not modify
  --validator PATH      Path to todo-kanban.sh (default: sibling script,
                        falls back to skills/.../todo-kanban.sh)
  -h, --help            Show this help and exit

Exit codes:
  0  item promoted (and validation passed)
  1  no match / multiple matches / validation failed
  2  usage error
EOF
}

title_substr=""
summary=""
file="TODO.md"
date_override=""
dry_run=0
validator=""

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --title) title_substr="$2"; shift 2 ;;
    --summary) summary="$2"; shift 2 ;;
    --file) file="$2"; shift 2 ;;
    --date) date_override="$2"; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    --validator) validator="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$title_substr" ] || [ -z "$summary" ]; then
  echo "Error: --title and --summary are required" >&2
  usage >&2
  exit 2
fi

if [ ! -f "$file" ]; then
  echo "Error: file not found: $file" >&2
  exit 1
fi

if [ -z "$validator" ]; then
  script_dir="$(cd "$(dirname "$0")" && pwd)"
  if [ -x "$script_dir/todo-kanban.sh" ]; then
    validator="$script_dir/todo-kanban.sh"
  fi
fi

if [ -n "$date_override" ]; then
  if ! [[ "$date_override" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    echo "Error: --date must be YYYY-MM-DD" >&2
    exit 2
  fi
  today="$date_override"
else
  today="$(date -u +%Y-%m-%d)"
fi

# Find the matching active item.
# Active line shape:
#   - [ ] **[Effort] Title** — description       (P1/P2/P3)
#   - [ ] **[?/Effort] Title** — description     (P?)
#
# Build a list of (line_no, lane, effort, title, raw) for active items whose
# title contains title_substr.

current_lane=""
match_count=0
match_line_no=0
match_lane=""
match_effort=""
match_title=""
match_priority=""

line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$(( line_no + 1 ))
  case "$line" in
    "## P1") current_lane="P1"; continue ;;
    "## P2") current_lane="P2"; continue ;;
    "## P3") current_lane="P3"; continue ;;
    "## P?") current_lane="P?"; continue ;;
    "## Done") current_lane="Done"; continue ;;
    "## "*) current_lane="post-done"; continue ;;
  esac

  # Only consider active items in P1/P2/P3/P?
  case "$current_lane" in
    P1|P2|P3)
      if [[ "$line" =~ ^-\ \[\ \]\ \*\*\[(S|M|L|XL)\]\ (.+)\*\*\ —\ (.+)$ ]]; then
        eff="${BASH_REMATCH[1]}"
        ttl="${BASH_REMATCH[2]}"
        case "$ttl" in
          *"$title_substr"*)
            match_count=$(( match_count + 1 ))
            match_line_no=$line_no
            match_lane="$current_lane"
            match_effort="$eff"
            match_title="$ttl"
            match_priority="$current_lane"
            ;;
        esac
      fi
      ;;
    "P?")
      if [[ "$line" =~ ^-\ \[\ \]\ \*\*\[\?/(S|M|L|XL)\]\ (.+)\*\*\ —\ (.+)$ ]]; then
        eff="${BASH_REMATCH[1]}"
        ttl="${BASH_REMATCH[2]}"
        case "$ttl" in
          *"$title_substr"*)
            match_count=$(( match_count + 1 ))
            match_line_no=$line_no
            match_lane="P?"
            match_effort="$eff"
            match_title="$ttl"
            match_priority="P?"
            ;;
        esac
      fi
      ;;
  esac
done < "$file"

if [ "$match_count" -eq 0 ]; then
  echo "Error: no active item title contains: $title_substr" >&2
  exit 1
fi
if [ "$match_count" -gt 1 ]; then
  echo "Error: $match_count active items match '$title_substr'; refine the substring" >&2
  exit 1
fi

new_done_line="- ✅ [$today] [$match_priority/$match_effort] $match_title — $summary"

# Build the new file: drop the matched line; insert new_done_line right after `## Done` header.
tmp="$(mktemp -t promote-todo.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

cur_lane=""
inserted=0
ln=0
while IFS= read -r line || [ -n "$line" ]; do
  ln=$(( ln + 1 ))
  if [ "$ln" -eq "$match_line_no" ]; then
    continue   # drop the matched active item
  fi
  printf '%s\n' "$line" >> "$tmp"
  if [ "$inserted" -eq 0 ] && [ "$line" = "## Done" ]; then
    printf '\n%s\n' "$new_done_line" >> "$tmp"
    inserted=1
  fi
done < "$file"

if [ "$inserted" -eq 0 ]; then
  echo "Error: could not find '## Done' heading in $file" >&2
  exit 1
fi

if [ "$dry_run" -eq 1 ]; then
  cat "$tmp"
  exit 0
fi

# Backup, swap, validate.
backup="$file.promote-bak.$$"
cp "$file" "$backup"
mv "$tmp" "$file"
trap - EXIT

if [ -n "$validator" ] && [ -x "$validator" ]; then
  if ! "$validator" --validate-only "$file" >/dev/null 2>&1; then
    echo "Error: validation failed after edit; restoring original $file" >&2
    "$validator" --validate-only "$file" >&2 || true
    mv "$backup" "$file"
    exit 1
  fi
fi

rm -f "$backup"
echo "Promoted: $match_priority/$match_effort '$match_title' → ## Done [$today]" >&2
