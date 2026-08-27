#!/usr/bin/env bash
set -euo pipefail

# Triage backlog/inbox.md into TODO.md by formalizing each non-empty,
# non-comment line via add-todo.sh and removing it from the inbox.
#
# Two modes:
#   interactive (default) — prompt for priority/effort/title/description
#                            for each line.
#   --batch               — only formalize lines whose key=value pairs are
#                            fully parseable; leave ambiguous lines in place.
#
# Inbox-line conventions:
#
#   # Comments and blank lines are ignored.
#   - free-form text                                    (interactive only)
#   - priority=P3 effort=M title="X" description="Y"    (batch-able)
#   - p=P3 e=M t="X" d="Y"                              (short aliases)
#
# Compatibility: macOS system Bash 3.2.

usage() {
  cat <<'EOF'
Usage: sweep-inbox.sh [OPTIONS]

Formalize entries in backlog/inbox.md into TODO.md by calling add-todo.sh
for each non-empty line. Successfully formalized lines are removed from
the inbox.

Options:
  --inbox PATH         Inbox file (default: backlog/inbox.md).
  --add-todo PATH      Path to add-todo.sh
                       (default: sibling script).
  --batch              Non-interactive: only process lines whose
                       priority / effort / title / description are all
                       parseable from key=value pairs. Ambiguous lines
                       stay in the inbox.
  --dry-run            Show what would be done; don't modify either file.
  -h, --help           Show this help and exit.

Exit codes:
  0  All processable lines were formalized (some may have been left in
     the inbox if --batch and key=value parsing failed).
  1  Inbox file not found / add-todo.sh not found / processing aborted.
  2  Usage error.
EOF
}

inbox="backlog/inbox.md"
add_todo=""
batch=0
dry_run=0

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --inbox) inbox="$2"; shift 2 ;;
    --add-todo) add_todo="$2"; shift 2 ;;
    --batch) batch=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ ! -f "$inbox" ]; then
  echo "Error: inbox file not found: $inbox" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

if [ -z "$add_todo" ]; then
  for cand in \
    "$script_dir/add-todo.sh" \
    "$repo_root/scripts/add-todo.sh" \
    "$repo_root/skills/local/project-knowledge-harness/scripts/add-todo.sh"
  do
    if [ -x "$cand" ]; then
      add_todo="$cand"
      break
    fi
  done
fi

if [ -z "$add_todo" ] || [ ! -x "$add_todo" ]; then
  echo "Error: add-todo.sh not found; pass --add-todo explicitly" >&2
  exit 1
fi

# Parse a single key=value-ish line into priority/effort/title/description.
# Sets these globals on success: parsed_priority, parsed_effort, parsed_title, parsed_description
# Returns 0 if all four are present, 1 otherwise.
parse_kv_line() {
  local line="$1"
  parsed_priority=""
  parsed_effort=""
  parsed_title=""
  parsed_description=""

  # Strip leading list bullet
  case "$line" in
    "- "*) line="${line#- }" ;;
  esac

  # Pull out quoted title="..." and description="..." first (greedy quoted),
  # since they may contain spaces.
  if [[ "$line" =~ (title|t)=\"([^\"]*)\" ]]; then
    parsed_title="${BASH_REMATCH[2]}"
  fi
  if [[ "$line" =~ (description|desc|d)=\"([^\"]*)\" ]]; then
    parsed_description="${BASH_REMATCH[2]}"
  fi
  if [[ "$line" =~ (priority|p)=([A-Za-z0-9?]+) ]]; then
    parsed_priority="${BASH_REMATCH[2]}"
  fi
  if [[ "$line" =~ (effort|e)=([A-Za-z0-9?]+) ]]; then
    parsed_effort="${BASH_REMATCH[2]}"
  fi

  if [ -n "$parsed_priority" ] && [ -n "$parsed_effort" ] && [ -n "$parsed_title" ] && [ -n "$parsed_description" ]; then
    return 0
  fi
  return 1
}

# Read inbox into a Bash 3.2-friendly array of lines.
lines_count=0
lines=()
while IFS= read -r ln || [ -n "$ln" ]; do
  lines[$lines_count]="$ln"
  lines_count=$(( lines_count + 1 ))
done < "$inbox"

# Walk lines; for each candidate, attempt to formalize.
remaining_count=0
remaining=()
processed=0
skipped=0
in_fence=0

i=0
while [ $i -lt $lines_count ]; do
  raw="${lines[$i]}"
  # Strip windows newlines defensively.
  raw="${raw%$'\r'}"

  # Track fenced code blocks so example lines inside ``` ``` are passed
  # through untouched (and not interpreted as candidates).
  case "$raw" in
    '```'*|'    ```'*)
      in_fence=$(( 1 - in_fence ))
      remaining[$remaining_count]="$raw"
      remaining_count=$(( remaining_count + 1 ))
      i=$(( i + 1 ))
      continue
      ;;
  esac
  if [ "$in_fence" -eq 1 ]; then
    remaining[$remaining_count]="$raw"
    remaining_count=$(( remaining_count + 1 ))
    i=$(( i + 1 ))
    continue
  fi

  # Always preserve blank lines, comments, and HTML comments untouched
  # (so the maintainer's prose / scaffolding inside the inbox is preserved).
  case "$raw" in
    ""|"#"*|"<!--"*|"-->"*)
      remaining[$remaining_count]="$raw"
      remaining_count=$(( remaining_count + 1 ))
      i=$(( i + 1 ))
      continue
      ;;
  esac

  # Lines that don't start with a list bullet are also passed through —
  # they're prose, not candidate entries.
  case "$raw" in
    "- "*) ;;
    *)
      remaining[$remaining_count]="$raw"
      remaining_count=$(( remaining_count + 1 ))
      i=$(( i + 1 ))
      continue
      ;;
  esac

  if parse_kv_line "$raw"; then
    if [ "$dry_run" -eq 1 ]; then
      echo "[dry-run] would formalize: $raw" >&2
      echo "[dry-run]   -> add-todo.sh --priority $parsed_priority --effort $parsed_effort --title \"$parsed_title\" --description \"$parsed_description\"" >&2
      processed=$(( processed + 1 ))
      i=$(( i + 1 ))
      continue
    fi
    if "$add_todo" --priority "$parsed_priority" --effort "$parsed_effort" --title "$parsed_title" --description "$parsed_description"; then
      processed=$(( processed + 1 ))
    else
      echo "Warning: add-todo.sh refused: $raw — keeping in inbox" >&2
      remaining[$remaining_count]="$raw"
      remaining_count=$(( remaining_count + 1 ))
      skipped=$(( skipped + 1 ))
    fi
    i=$(( i + 1 ))
    continue
  fi

  # Not parseable. In batch mode, leave it. In interactive mode, prompt.
  if [ "$batch" -eq 1 ]; then
    remaining[$remaining_count]="$raw"
    remaining_count=$(( remaining_count + 1 ))
    skipped=$(( skipped + 1 ))
    i=$(( i + 1 ))
    continue
  fi

  if [ ! -t 0 ]; then
    echo "Warning: stdin is not a TTY and no key=value pairs on line — leaving in inbox: $raw" >&2
    remaining[$remaining_count]="$raw"
    remaining_count=$(( remaining_count + 1 ))
    skipped=$(( skipped + 1 ))
    i=$(( i + 1 ))
    continue
  fi

  echo "" >&2
  echo "Inbox line: $raw" >&2
  printf 'Action ([f]ormalize / [s]kip / [d]elete / [q]uit) [s]: ' >&2
  read -r action </dev/tty || action="q"
  case "$action" in
    q|Q) echo "Aborting; remaining lines kept in inbox." >&2; break ;;
    d|D)
      echo "Deleted from inbox without formalizing: $raw" >&2
      processed=$(( processed + 1 ))
      i=$(( i + 1 ))
      continue
      ;;
    f|F) ;;  # fall through to prompts
    *)
      remaining[$remaining_count]="$raw"
      remaining_count=$(( remaining_count + 1 ))
      skipped=$(( skipped + 1 ))
      i=$(( i + 1 ))
      continue
      ;;
  esac

  printf 'Priority {P1|P2|P3|P?} [P?]: ' >&2; read -r p </dev/tty || p=""
  printf 'Effort {S|M|L|XL|?} [?]: ' >&2; read -r e </dev/tty || e=""
  printf 'Title: ' >&2; read -r t </dev/tty || t=""
  printf 'Description: ' >&2; read -r d </dev/tty || d=""
  p="${p:-P?}"
  e="${e:-?}"
  if [ -z "$t" ] || [ -z "$d" ]; then
    echo "Title and description are required; skipping." >&2
    remaining[$remaining_count]="$raw"
    remaining_count=$(( remaining_count + 1 ))
    skipped=$(( skipped + 1 ))
    i=$(( i + 1 ))
    continue
  fi

  if [ "$dry_run" -eq 1 ]; then
    echo "[dry-run] would call: add-todo.sh --priority $p --effort $e --title \"$t\" --description \"$d\"" >&2
    processed=$(( processed + 1 ))
    i=$(( i + 1 ))
    continue
  fi

  if "$add_todo" --priority "$p" --effort "$e" --title "$t" --description "$d"; then
    processed=$(( processed + 1 ))
  else
    echo "Warning: add-todo.sh refused; keeping line in inbox." >&2
    remaining[$remaining_count]="$raw"
    remaining_count=$(( remaining_count + 1 ))
    skipped=$(( skipped + 1 ))
  fi
  i=$(( i + 1 ))
done

# If we broke early via 'q', append untouched lines.
while [ $i -lt $lines_count ]; do
  remaining[$remaining_count]="${lines[$i]}"
  remaining_count=$(( remaining_count + 1 ))
  i=$(( i + 1 ))
done

if [ "$dry_run" -eq 1 ]; then
  echo "[dry-run] processed=$processed skipped=$skipped remaining=$remaining_count" >&2
  exit 0
fi

# Write the new inbox (if anything changed).
if [ "$processed" -gt 0 ]; then
  tmp="$(mktemp -t sweep-inbox.XXXXXX)"
  trap 'rm -f "$tmp"' EXIT
  j=0
  while [ $j -lt $remaining_count ]; do
    printf '%s\n' "${remaining[$j]}" >> "$tmp"
    j=$(( j + 1 ))
  done
  mv "$tmp" "$inbox"
  trap - EXIT
fi

echo "Sweep complete: processed=$processed skipped=$skipped remaining=$remaining_count" >&2
