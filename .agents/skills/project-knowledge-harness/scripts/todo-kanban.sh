#!/usr/bin/env bash
set -euo pipefail

# Validate a TODO.md that follows the project-knowledge-harness format and
# render a kanban-style Markdown board to stdout.
#
# Compatibility: macOS system Bash 3.2 (no associative arrays, no readarray).

usage() {
  cat <<'EOF'
Usage: todo-kanban.sh [OPTIONS] [TODO_FILE]

Validate a TODO.md that follows the project-knowledge-harness format and
render a kanban-style Markdown board.

Required structure:
  - First non-empty heading must be `# TODO`
  - Sections must appear in order: ## P1, ## P2, ## P3, ## P?, ## Done
  - Top-level list items inside sections are validated:
      Active (P1/P2/P3): - [ ] **[Effort] Title** — description
      P? items:          - [ ] **[?/Effort] Title** — description
      Done items:        - ✅ [YYYY-MM-DD] [P#/Effort] Title — summary
    where Effort is one of S, M, L, XL.
    Active items may end with: → [research](backlog/<slug>.md)

  Anything that is NOT a top-level `- [ ]` / `- ✅` item — prose paragraphs,
  blockquotes, HTML comments, `---` rules, indented sub-bullets — is
  ignored by the validator. Only top-level items count toward the lane
  totals in the rendered board.

  After the `## Done` section, additional `## ...` headings (e.g. notes,
  prune log) are allowed and skipped without validation.

Options:
  -h, --help           Show this help and exit
  --validate-only      Validate without rendering the board
  --json               Emit a JSON summary instead of the Markdown board
                       (counts per lane + items per lane)

Exit codes:
  0  validation passed (and board rendered if not --validate-only)
  1  validation failed (offending file:line printed to stderr)
  2  usage error
EOF
}

mode_render="markdown"
validate_only=0
todo_file=""

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --validate-only) validate_only=1; shift ;;
    --json) mode_render="json"; shift ;;
    --) shift; break ;;
    -*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    *)
      if [ -n "$todo_file" ]; then
        echo "Error: only one TODO_FILE positional argument accepted" >&2
        usage >&2
        exit 2
      fi
      todo_file="$1"
      shift
      ;;
  esac
done

todo_file="${todo_file:-TODO.md}"

if [ ! -f "$todo_file" ]; then
  echo "Error: file not found: $todo_file" >&2
  exit 1
fi

error() {
  local line_no="$1"
  local message="$2"
  echo "Syntax error in $todo_file:$line_no: $message" >&2
  exit 1
}

# Active item regex (P1/P2/P3 lanes)
active_re='^- \[ \] \*\*\[(S|M|L|XL)\] [^*]+\*\* — .+$'
# P? lane item regex
peq_re='^- \[ \] \*\*\[\?/(S|M|L|XL)\] [^*]+\*\* — .+$'
# Done item regex
done_re='^- ✅ \[[0-9]{4}-[0-9]{2}-[0-9]{2}\] \[(P1|P2|P3|P\?)/(S|M|L|XL)\] .+ — .+$'

# Parallel arrays indexed by lane (0..4)
lane_names=("P1" "P2" "P3" "P?" "Done")
lane_count=(0 0 0 0 0)
lane_items=("" "" "" "" "")

append_item() {
  local idx="$1"
  local item="$2"
  if [ -n "${lane_items[$idx]}" ]; then
    lane_items[$idx]="${lane_items[$idx]}"$'\n'"$item"
  else
    lane_items[$idx]="$item"
  fi
  lane_count[$idx]=$(( lane_count[$idx] + 1 ))
}

title_seen=0
section_idx=-1            # which lane we're currently inside (-1 = before P1)
expected_next=0           # next expected lane index (0..5; 5 = past Done)
post_done=0               # 1 once we've left the Done lane (extra ## headings ok)
line_no=0

while IFS= read -r line || [ -n "$line" ]; do
  line_no=$(( line_no + 1 ))

  # Title
  if [ "$line" = "# TODO" ]; then
    if [ "$title_seen" -eq 1 ]; then
      error "$line_no" "duplicate '# TODO' heading"
    fi
    title_seen=1
    continue
  fi

  # Other top-level # heading is forbidden (keeps the file single-purpose)
  case "$line" in
    "# "*|"#"[!#]*)
      if [ "$line" != "# TODO" ]; then
        error "$line_no" "unexpected top-level '# ...' heading; only '# TODO' is allowed"
      fi
      ;;
  esac

  # Section heading
  case "$line" in
    "## "*)
      heading="${line#"## "}"
      if [ "$post_done" -eq 1 ]; then
        # Allow any extra headings after Done; do not validate further items
        section_idx=-2
        continue
      fi
      if [ "$expected_next" -ge "${#lane_names[@]}" ]; then
        # Should have entered post_done already; defensive
        section_idx=-2
        continue
      fi
      expected="${lane_names[$expected_next]}"
      if [ "$heading" != "$expected" ]; then
        error "$line_no" "expected section heading '## $expected', got '## $heading'"
      fi
      section_idx=$expected_next
      expected_next=$(( expected_next + 1 ))
      if [ "$expected" = "Done" ]; then
        # Mark that the next ## may legally appear (extra notes section)
        post_done=1
      fi
      continue
      ;;
  esac

  # Past Done section: skip validation
  if [ "$section_idx" -eq -2 ]; then
    continue
  fi

  # Before any section: skip prose/blockquotes freely
  if [ "$section_idx" -eq -1 ]; then
    continue
  fi

  # Indented content (sub-bullets, continuation): skip without validating
  case "$line" in
    " "*|$'\t'*) continue ;;
  esac

  # Top-level list item starting with `- ` — validate
  case "$line" in
    "- "*)
      current_lane="${lane_names[$section_idx]}"
      if [ "$current_lane" = "Done" ]; then
        if ! [[ "$line" =~ $done_re ]]; then
          error "$line_no" "Done item must match: '- ✅ [YYYY-MM-DD] [P#/Effort] Title — summary'"
        fi
      elif [ "$current_lane" = "P?" ]; then
        if ! [[ "$line" =~ $peq_re ]]; then
          error "$line_no" "P? item must match: '- [ ] **[?/Effort] Title** — description'"
        fi
      else
        if ! [[ "$line" =~ $active_re ]]; then
          error "$line_no" "active item must match: '- [ ] **[Effort] Title** — description'"
        fi
      fi
      append_item "$section_idx" "$line"
      continue
      ;;
  esac

  # Anything else (prose, blockquote, ---, HTML comment, blank): skip silently.
done < "$todo_file"

if [ "$title_seen" -eq 0 ]; then
  error 1 "missing '# TODO' heading"
fi

if [ "$expected_next" -ne "${#lane_names[@]}" ]; then
  missing="${lane_names[$expected_next]}"
  error "$line_no" "missing section heading '## $missing'"
fi

if [ "$validate_only" -eq 1 ]; then
  echo "OK: $todo_file passes project-knowledge-harness format" >&2
  exit 0
fi

if [ "$mode_render" = "json" ]; then
  printf '{\n'
  printf '  "source": "%s",\n' "$todo_file"
  printf '  "lanes": [\n'
  i=0
  while [ $i -lt ${#lane_names[@]} ]; do
    printf '    {"name": "%s", "count": %d, "items": [' "${lane_names[$i]}" "${lane_count[$i]}"
    if [ "${lane_count[$i]}" -gt 0 ]; then
      printf '\n'
      first=1
      # Iterate items separated by newline
      while IFS= read -r itm; do
        # JSON-escape backslash and double-quote
        esc="${itm//\\/\\\\}"
        esc="${esc//\"/\\\"}"
        if [ $first -eq 1 ]; then
          printf '      "%s"' "$esc"
          first=0
        else
          printf ',\n      "%s"' "$esc"
        fi
      done <<EOF
${lane_items[$i]}
EOF
      printf '\n    '
    fi
    if [ $i -lt $(( ${#lane_names[@]} - 1 )) ]; then
      printf ']},\n'
    else
      printf ']}\n'
    fi
    i=$(( i + 1 ))
  done
  printf '  ]\n}\n'
  exit 0
fi

# Markdown render
printf '# TODO Kanban\n\n'
printf '_Source: `%s`_\n\n' "$todo_file"
printf '| Lane | Count |\n'
printf '|---|---:|\n'
i=0
while [ $i -lt ${#lane_names[@]} ]; do
  printf '| `%s` | %s |\n' "${lane_names[$i]}" "${lane_count[$i]}"
  i=$(( i + 1 ))
done
printf '\n'

i=0
while [ $i -lt ${#lane_names[@]} ]; do
  printf '## %s (%s)\n\n' "${lane_names[$i]}" "${lane_count[$i]}"
  if [ "${lane_count[$i]}" -eq 0 ]; then
    printf '_No items._\n\n'
  else
    printf '%s\n\n' "${lane_items[$i]}"
  fi
  i=$(( i + 1 ))
done
