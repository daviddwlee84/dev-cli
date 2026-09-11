#!/usr/bin/env bash
# scan-staged.sh — run gitleaks on the current staged diff with
# agent-friendly exit codes.
#
# Bash 3.2 compatible (stock macOS).

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scan-staged.sh [OPTIONS]

Scan the staged diff for secrets using `gitleaks git --staged`. Intended
as the "last line of defense" wrapper agents call before `git commit` so
they can branch on structured exit codes.

Options:
  --redact           Use the legacy exit-10 result for masked findings.
                     Output is always redacted; files are never changed.
  --no-redact        Use the legacy exit-20 result; output remains redacted.
  --config PATH      Path to .gitleaks.toml (default: repo root if present).
  --verbose          Print gitleaks' own output to stderr.
  --help, -h         Show this help and exit.

Output (stdout):
  - If clean: no output.
  - If leaks found: one JSON object per line with the finding details.

Exit codes:
  0   clean — no secrets found
  10  leaks found (and redacted if --redact was passed)
  20  leaks found, not redacted (caller must rotate + re-stage)
  30  gitleaks binary not installed
  40  gitleaks error (bad config, unreadable repo, etc.)
  1   invalid arguments
  2   not inside a git repo
EOF
}

log()  { printf '%s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit "${2:-1}"; }

REDACT=0
CONFIG=""
VERBOSE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --redact)     REDACT=1; shift ;;
    --no-redact)  REDACT=0; shift ;;
    --config)     CONFIG="${2:-}"; shift 2 ;;
    --config=*)   CONFIG="${1#--config=}"; shift ;;
    --verbose)    VERBOSE=1; shift ;;
    --help|-h)    usage; exit 0 ;;
    -*)           die "unknown flag: $1 (try --help)" 1 ;;
    *)            die "unexpected positional arg: $1 (try --help)" 1 ;;
  esac
done

# Check prerequisites.
if ! command -v gitleaks >/dev/null 2>&1; then
  log "gitleaks not installed. Install hints:"
  log "  macOS:   brew install gitleaks"
  log "  Linux:   https://github.com/gitleaks/gitleaks/releases"
  exit 30
fi

if ! git rev-parse --show-toplevel >/dev/null 2>&1; then
  die "not inside a git repo" 2
fi
cd "$(git rev-parse --show-toplevel)"

# Auto-discover config if not specified.
if [ -z "$CONFIG" ] && [ -f ".gitleaks.toml" ]; then
  CONFIG=".gitleaks.toml"
fi

report_path="$(mktemp -t gitleaks-report.XXXXXX.json)"
trap 'rm -f "$report_path"' EXIT

# Assemble the command. Post v8.19.0 `gitleaks git --staged` replaces the
# deprecated `gitleaks protect --staged`.
cmd=(gitleaks git --staged
     --report-format json
     --report-path "$report_path"
     --exit-code 0 --redact=100)
[ -n "$CONFIG" ] && cmd+=(--config "$CONFIG")


# Capture gitleaks' own exit code — with `--exit-code 0` it returns 0
# whether leaks are found or not, so a non-zero exit means the tool itself
# failed (config error, unreadable repo, etc.).
stderr_capture="$(mktemp -t gitleaks-stderr.XXXXXX)"
trap 'rm -f "$report_path" "$stderr_capture"' EXIT

gl_rc=0
if [ "$VERBOSE" = "1" ]; then
  "${cmd[@]}" >/dev/null 2>"$stderr_capture" || gl_rc=$?
else
  "${cmd[@]}" >/dev/null 2>"$stderr_capture" || gl_rc=$?
fi

if [ "$gl_rc" -ne 0 ]; then
  log "gitleaks failed to run (exit $gl_rc). Tail of its stderr:"
  log "Scanner diagnostics are withheld because they may contain private input."
  exit 40
fi

# Gitleaks writes an empty file when truly clean, or `[]` when the report
# format is JSON and there are zero findings. Normalize both to "clean".
if [ ! -s "$report_path" ]; then
  exit 0
fi

finding_count=0
if command -v python3 >/dev/null 2>&1; then
  finding_count=$(python3 - "$report_path" <<'PY'
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        data = json.load(f)
except (json.JSONDecodeError, OSError):
    sys.exit(40)
if not isinstance(data, list):
    sys.exit(40)
print(len(data))
PY
)
else
  log "python3 is required to validate and sanitize scanner output"
  exit 40
fi

if [ "$finding_count" -eq 0 ]; then
  exit 0
fi

# Re-emit each finding as its own JSON object (one per line) for ergonomic
# agent consumption. Findings file is a JSON *array*; slice it into lines
# using python3 — avoid adding a jq dependency.
if command -v python3 >/dev/null 2>&1; then
  python3 - "$report_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    data = json.load(f)
for finding in data:
    # Keep only the most useful fields; caller can re-open the raw report.
    slim = {
        "rule_id": finding.get("RuleID"),
        "file": finding.get("File"),
        "line": finding.get("StartLine"),
        "commit": finding.get("Commit") or "STAGED",
        "fingerprint": finding.get("Fingerprint"),
    }
    print(json.dumps(slim, ensure_ascii=False))
PY
else
  log "python3 is required for safe scanner output"
  exit 40
fi

if [ "$REDACT" = "1" ]; then
  log "gitleaks found candidate leaks; output is masked."
  log "  NOTE: gitleaks --redact only masks the CLI output, it does NOT"
  log "        rewrite files. Use redact_secrets.py --fix (or the pre-commit"
  log "        redact-agent-secrets hook) to actually scrub the artifacts."
  exit 10
else
  log "gitleaks found leaks. See JSON findings on stdout."
  log "Next: rotate the secret at the provider, then run redact_secrets.py --fix,"
  log "      then re-stage and re-run this check. DO NOT \`git push --force\`."
  exit 20
fi
