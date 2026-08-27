#!/usr/bin/env bash
# lint-frontmatter.sh — Validate SKILL.md YAML frontmatter across a whole tree.
#
# Why this exists: every agent harness (`npx skills`, Claude Code, Cursor,
# Codex) parses SKILL.md frontmatter with a real YAML parser and **silently
# skips** any skill whose frontmatter fails to parse. `lint-skill.sh` reads
# frontmatter with a permissive awk extractor, so it happily passes files
# that real parsers reject. This script closes that gap.
#
# The trap that motivated it — an unquoted `description:` containing ": ":
#
#   description: Use when you debug a FastAPI app: choosing def vs async def
#                                                ^ YAML reads a nested mapping
#     npx skills → "YAML parse error: Nested mappings are not allowed in
#                   compact mappings"
#
# Fix: wrap the value in single quotes.
#
# Checks per SKILL.md:
#   1. Frontmatter delimiters — `---` on line 1, closing `---` below it
#   2. Frontmatter parses as YAML (yq / PyYAML / js-yaml, whichever is found)
#   3. Root is a mapping; `name` and `description` present as plain strings
#   4. (warn) unquoted plain scalar containing " #" — YAML treats the rest of
#      the line as a comment and silently truncates the value
#
# Bash 3.2 compatible (stock macOS). No hard dependency: with no YAML parser
# on PATH it degrades to the pattern heuristic and says so.

set -uo pipefail

usage() {
  cat <<'EOF'
Usage: lint-frontmatter.sh [OPTIONS] [PATH...]

Validate the YAML frontmatter of every SKILL.md under PATH (default: ".").
PATH may be a SKILL.md file or a directory to search recursively.

Options:
  --parser NAME      Force a parser: auto (default), yq, python3 (PyYAML),
                     node (the js "yaml" package — what `npx skills` uses).
  --quiet            Only print failures and the summary line.
  --help, -h         Show this help and exit.

Examples:
  bash lint-frontmatter.sh                    # lint every SKILL.md under cwd
  bash lint-frontmatter.sh skills             # lint a collection directory
  bash lint-frontmatter.sh skills/local/my-skill/SKILL.md

Exit codes:
  0  every SKILL.md parses and has string name + description
  1  at least one file failed
  2  invalid arguments / path not found / forced parser unavailable
EOF
}

QUIET=0
FORCED_PARSER="auto"
PATHS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --parser)  [ $# -ge 2 ] || { printf 'error: --parser needs a value (auto|yq|python3|node)\n' >&2; exit 2; }
               FORCED_PARSER="$2"; shift 2 ;;
    --parser=*) FORCED_PARSER="${1#--parser=}"; shift ;;
    --quiet)   QUIET=1; shift ;;
    --help|-h) usage; exit 0 ;;
    -*)        printf 'error: unknown flag: %s (try --help)\n' "$1" >&2; exit 2 ;;
    *)         PATHS+=("$1"); shift ;;
  esac
done

[ ${#PATHS[@]} -eq 0 ] && PATHS=(".")

# ----------------------------------------------------------------------
# Parser detection — prefer the strictest thing available.
# ----------------------------------------------------------------------
PARSER="none"
case "$FORCED_PARSER" in
  auto) ;;
  yq)      command -v yq >/dev/null 2>&1 || { printf 'error: yq not found on PATH\n' >&2; exit 2; }; PARSER="yq" ;;
  python3) python3 -c 'import yaml' >/dev/null 2>&1 || { printf 'error: python3 with PyYAML not available\n' >&2; exit 2; }; PARSER="python3" ;;
  node)    node -e "require('yaml')" >/dev/null 2>&1 || { printf 'error: node with the "yaml" package not available (try NODE_PATH=...)\n' >&2; exit 2; }; PARSER="node" ;;
  *)       printf 'error: unknown --parser: %s (auto|yq|python3|node)\n' "$FORCED_PARSER" >&2; exit 2 ;;
esac

if [ "$PARSER" = "none" ]; then
  if command -v yq >/dev/null 2>&1; then
    PARSER="yq"
  elif command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
    PARSER="python3"
  elif command -v node >/dev/null 2>&1 && node -e "require('yaml')" >/dev/null 2>&1; then
    PARSER="node"
  fi
fi

# probe_* contract: print one line to stdout, either
#   OK|<root_type>|<name_type>|<desc_type>     types: map/str/null/other
#   FAIL|<first line of the parser error>
# Parser errors point at the extracted frontmatter (and, for yq, at the temp
# file). Drop the temp path and shift line numbers by 1 so they name the line
# in SKILL.md itself.
normalize_err() {
  sed -e "s|^Error: bad file '[^']*': ||" -e 's| in "[^"]*"||' | awk '
    {
      out = ""; s = $0
      while (match(s, /line [0-9]+/)) {
        n = substr(s, RSTART + 5, RLENGTH - 5) + 1
        out = out substr(s, 1, RSTART - 1) "line " n
        s = substr(s, RSTART + RLENGTH)
      }
      print out s
    }'
}

probe_yq() {
  local tmp="$1" err root name desc
  if ! err=$(yq e '.' "$tmp" 2>&1 >/dev/null); then
    printf 'FAIL|%s\n' "$(printf '%s' "$err" | head -n1)"
    return
  fi
  root=$(yq e '. | tag' "$tmp" 2>/dev/null)
  name=$(yq e '.name | tag' "$tmp" 2>/dev/null)
  desc=$(yq e '.description | tag' "$tmp" 2>/dev/null)
  printf 'OK|%s|%s|%s\n' \
    "$(normalize_tag "$root")" "$(normalize_tag "$name")" "$(normalize_tag "$desc")"
}

normalize_tag() {
  case "$1" in
    '!!map') printf 'map' ;;
    '!!str') printf 'str' ;;
    '!!null'|'') printf 'null' ;;
    *) printf 'other' ;;
  esac
}

probe_python3() {
  python3 - "$1" <<'PY'
import sys, yaml
def t(v):
    if v is None: return "null"
    if isinstance(v, str): return "str"
    if isinstance(v, dict): return "map"
    return "other"
try:
    with open(sys.argv[1], encoding="utf-8") as fh:
        doc = yaml.safe_load(fh)
except Exception as exc:
    print("FAIL|" + str(exc).replace("\n", " ")[:200])
    sys.exit(0)
if isinstance(doc, dict):
    print("OK|map|%s|%s" % (t(doc.get("name")), t(doc.get("description"))))
else:
    print("OK|%s|null|null" % t(doc))
PY
}

probe_node() {
  node -e '
    const YAML = require("yaml"), fs = require("fs");
    const t = v => v === null || v === undefined ? "null"
      : typeof v === "string" ? "str"
      : (typeof v === "object" && !Array.isArray(v)) ? "map" : "other";
    let doc;
    try { doc = YAML.parse(fs.readFileSync(process.argv[1], "utf8")); }
    catch (e) { console.log("FAIL|" + String(e.message).split("\n")[0].slice(0, 200)); process.exit(0); }
    if (t(doc) === "map") console.log(`OK|map|${t(doc.name)}|${t(doc.description)}`);
    else console.log(`OK|${t(doc)}|null|null`);
  ' "$1"
}

probe() {
  local raw
  case "$PARSER" in
    yq)      raw=$(probe_yq "$1") ;;
    python3) raw=$(probe_python3 "$1") ;;
    node)    raw=$(probe_node "$1") ;;
    *)       raw='SKIP|no YAML parser on PATH' ;;
  esac
  case "$raw" in
    FAIL\|*) printf 'FAIL|%s\n' "$(printf '%s' "${raw#FAIL|}" | normalize_err)" ;;
    *)       printf '%s\n' "$raw" ;;
  esac
}

# Pattern heuristic: flag unquoted top-level plain scalars that YAML will
# either reject or silently mangle. Prints "reason" lines. Used as the hint
# when a parse fails, and as the only check when no parser is installed.
risky_scalars() {
  awk '
    /^[A-Za-z0-9_-]+:[ \t]+[^ \t]/ {
      key = $0; sub(/:.*/, "", key)
      val = $0; sub(/^[A-Za-z0-9_-]+:[ \t]*/, "", val)
      sub(/[ \t]+$/, "", val)
      first = substr(val, 1, 1)
      # Quoted or block scalars are already safe.
      if (first == "\"" || first == "'\''" || first == ">" || first == "|" \
          || first == "[" || first == "{") next
      if (val ~ /: / || val ~ /:$/)
        printf "  line %d: %s value contains \": \" — YAML reads it as a nested mapping; wrap the value in single quotes\n", NR + 1, key
      else if (val ~ / #/)
        printf "  line %d: %s value contains \" #\" — YAML starts a comment there and silently truncates the value; wrap it in single quotes\n", NR + 1, key
      else if (index("-?,[]{}#&*!|>%@`", first) > 0)
        printf "  line %d: %s value starts with the reserved YAML character %s — wrap the value in single quotes\n", NR + 1, key, first
    }
  ' "$1"
}

# ----------------------------------------------------------------------
# Collect SKILL.md files
# ----------------------------------------------------------------------
collect() {
  local p
  for p in "$@"; do
    if [ -f "$p" ]; then
      printf '%s\n' "$p"
    elif [ -d "$p" ]; then
      find "$p" -name SKILL.md \
        -not -path '*/node_modules/*' \
        -not -path '*/.git/*' \
        -not -path '*/site/*' 2>/dev/null
    else
      printf 'error: no such file or directory: %s\n' "$p" >&2
      exit 2
    fi
  done | sort -u
}

FILES=$(collect "${PATHS[@]}") || exit 2
if [ -z "$FILES" ]; then
  printf 'error: no SKILL.md found under: %s\n' "${PATHS[*]}" >&2
  exit 2
fi

TMP=$(mktemp -t skill-frontmatter.XXXXXX) || exit 2
trap 'rm -f "$TMP"' EXIT

CHECKED=0
FAILED=0
WARNED=0

say()  { [ "$QUIET" = "1" ] || printf '%s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$1"; shift; [ $# -gt 0 ] && printf '%s\n' "$@"; FAILED=$((FAILED + 1)); }

while IFS= read -r file; do
  [ -n "$file" ] || continue
  CHECKED=$((CHECKED + 1))

  # 1. Delimiters
  if [ "$(head -n1 "$file" | tr -d '\r')" != "---" ]; then
    fail "$file" "  no opening '---' on line 1 (frontmatter is missing)"
    continue
  fi
  close=$(awk 'NR>1 && /^---[ \t]*\r?$/ {print NR; exit}' "$file")
  if [ -z "$close" ]; then
    fail "$file" "  no closing '---' below the opening delimiter"
    continue
  fi
  sed -n "2,$((close - 1))p" "$file" > "$TMP"

  # 2-3. Parse + required string fields
  result=$(probe "$TMP")
  status=${result%%|*}
  rest=${result#*|}

  case "$status" in
    FAIL)
      hints=$(risky_scalars "$TMP")
      if [ -n "$hints" ]; then
        fail "$file" "  YAML parse error: $rest" "$hints"
      else
        fail "$file" "  YAML parse error: $rest"
      fi
      continue
      ;;
    SKIP)
      hints=$(risky_scalars "$TMP")
      if [ -n "$hints" ]; then
        fail "$file" "$hints"
      else
        say "ok    $file (heuristic only — no YAML parser on PATH)"
      fi
      continue
      ;;
  esac

  root=${rest%%|*}; rest=${rest#*|}
  name_t=${rest%%|*}; desc_t=${rest#*|}

  if [ "$root" != "map" ]; then
    fail "$file" "  frontmatter is not a YAML mapping (parsed as: $root)"
    continue
  fi

  problems=""
  [ "$name_t" = "str" ] || problems="$problems
  'name' must be a string (got: $name_t)"
  [ "$desc_t" = "str" ] || problems="$problems
  'description' must be a string (got: $desc_t)"

  if [ -n "$problems" ]; then
    fail "$file" "${problems#
}"
    continue
  fi

  # 4. Silent-truncation warning (parses fine, but loses text).
  warnings=$(risky_scalars "$TMP" | grep -- 'silently truncates' || true)
  if [ -n "$warnings" ]; then
    printf 'WARN  %s\n%s\n' "$file" "$warnings"
    WARNED=$((WARNED + 1))
  else
    say "ok    $file"
  fi
done <<EOF
$FILES
EOF

say ""
printf 'checked %d SKILL.md (parser: %s) — %d failed, %d warned\n' \
  "$CHECKED" "$PARSER" "$FAILED" "$WARNED"

if [ "$PARSER" = "none" ]; then
  printf 'note: no YAML parser found (install yq, PyYAML, or the js "yaml" package) — pattern heuristic only\n' >&2
fi

[ "$FAILED" -gt 0 ] && exit 1
exit 0
