#!/usr/bin/env bash
# test_scan_staged.sh — exit-code contract for scripts/scan-staged.sh.
#
# Asserts the documented exit codes:
#   0   clean
#   20  leaks found
#   30  gitleaks binary missing
#    2  not inside a git repo
#
# Bash 3.2 compatible (stock macOS).

set -u  # intentionally NOT -e: we capture non-zero exit codes

TESTS_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$TESTS_DIR/.." && pwd)"
SCAN="$SKILL_DIR/scripts/scan-staged.sh"
FIXTURES="$TESTS_DIR/fixtures"

# State (no associative arrays — bash 3.2 compat)
PASS_COUNT=0
FAIL_COUNT=0
FAIL_LOG=""

red()   { printf '\033[31m%s\033[0m' "$*"; }
green() { printf '\033[32m%s\033[0m' "$*"; }
dim()   { printf '\033[2m%s\033[0m' "$*"; }

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '  %s %s\n' "$(green PASS)" "$1"
}
fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf '  %s %s\n' "$(red FAIL)" "$1"
  FAIL_LOG="${FAIL_LOG}FAIL: $1\n"
}

# make_repo — spin up an isolated tmp repo with an initial commit.
# Prints the path to stdout.
make_repo() {
  local d
  d=$(mktemp -d /tmp/test-scan-staged.XXXXXX)
  git -C "$d" init -q -b main
  git -C "$d" -c user.email=test@example.com -c user.name=test \
      commit -q --allow-empty -m init
  printf '%s' "$d"
}

# Copy fixture + stage it under the given relative path. Strips the inline
# `<!-- gitleaks:allow -->` marker so the rules still fire in the throwaway repo
# (byte-identical to the fixture otherwise). Line-based sed → newline never
# enters the pattern space, so [[:space:]]* only eats the horizontal separator.
stage_fixture() {
  local repo="$1" fixture="$2" dest_rel="$3"
  mkdir -p "$(dirname "$repo/$dest_rel")"
  sed 's/[[:space:]]*<!-- *gitleaks:allow *-->//g' "$FIXTURES/$fixture" > "$repo/$dest_rel"
  git -C "$repo" add -- "$dest_rel"
}

if ! command -v gitleaks >/dev/null 2>&1; then
  printf '%s gitleaks binary missing\n' "$(dim SKIP)"
  printf '  (skipping positive cases; running the exit-30 contract only)\n\n'
  GITLEAKS_AVAILABLE=0
else
  GITLEAKS_AVAILABLE=1
fi

printf '== scan-staged exit-code contract ==\n\n'

# --- Case 1: clean file → exit 0 ---
if [ "$GITLEAKS_AVAILABLE" = "1" ]; then
  REPO=$(make_repo)
  # Install the gitleaks config so rules apply.
  cp "$SKILL_DIR/assets/gitleaks.toml.template" "$REPO/.gitleaks.toml"
  stage_fixture "$REPO" "clean.md" "notes.md"
  (cd "$REPO" && bash "$SCAN" >/dev/null 2>&1)
  rc=$?
  if [ "$rc" = "0" ]; then pass "clean fixture → exit 0"
  else fail "clean fixture → exit 0 (got $rc)"
  fi
  rm -rf "$REPO"
fi

# --- Case 2: real leak inside .claude/plans/ → exit 20 ---
if [ "$GITLEAKS_AVAILABLE" = "1" ]; then
  REPO=$(make_repo)
  cp "$SKILL_DIR/assets/gitleaks.toml.template" "$REPO/.gitleaks.toml"
  stage_fixture "$REPO" "real_anthropic.md" ".claude/plans/p1.md"
  (cd "$REPO" && bash "$SCAN" >/dev/null 2>&1)
  rc=$?
  if [ "$rc" = "20" ]; then pass "real Anthropic key → exit 20"
  else fail "real Anthropic key → exit 20 (got $rc)"
  fi
  rm -rf "$REPO"
fi

# --- Case 3: not in a git repo → exit 2 ---
OUTSIDE=$(mktemp -d /tmp/test-scan-outside.XXXXXX)
(cd "$OUTSIDE" && bash "$SCAN" >/dev/null 2>&1)
rc=$?
if [ "$rc" = "2" ]; then pass "outside git repo → exit 2"
else fail "outside git repo → exit 2 (got $rc)"
fi
rm -rf "$OUTSIDE"

# --- Case 4: gitleaks missing → exit 30 ---
# Simulate by running with an empty PATH so `command -v gitleaks` fails.
REPO=$(make_repo)
cp "$SKILL_DIR/assets/gitleaks.toml.template" "$REPO/.gitleaks.toml" 2>/dev/null || true
# Keep /bin and /usr/bin so the script itself (bash, git, mktemp) runs.
FAKE_PATH="/bin:/usr/bin"
(cd "$REPO" && PATH="$FAKE_PATH" bash "$SCAN" >/dev/null 2>&1)
rc=$?
if [ "$rc" = "30" ]; then pass "no gitleaks on PATH → exit 30"
else fail "no gitleaks on PATH → exit 30 (got $rc)"
fi
rm -rf "$REPO"

# --- Case 5: empty JSON report `[]` handled as clean → exit 0 ---
# This is the regression we caught during initial verification: gitleaks
# writes `[]` for zero findings; scan-staged.sh must not treat that as
# "non-empty file → leaks found".
if [ "$GITLEAKS_AVAILABLE" = "1" ]; then
  REPO=$(make_repo)
  cp "$SKILL_DIR/assets/gitleaks.toml.template" "$REPO/.gitleaks.toml"
  stage_fixture "$REPO" "example_shapes.md" ".claude/plans/p1.md"
  (cd "$REPO" && bash "$SCAN" >/dev/null 2>&1)
  rc=$?
  if [ "$rc" = "0" ]; then pass "allowlisted example shapes → exit 0 (empty [] report)"
  else fail "allowlisted example shapes → exit 0 (got $rc)"
  fi
  rm -rf "$REPO"
fi

printf '\n== summary ==\n'
printf 'pass: %d\nfail: %d\n' "$PASS_COUNT" "$FAIL_COUNT"
if [ "$FAIL_COUNT" -gt 0 ]; then
  printf '\n%b' "$FAIL_LOG"
  exit 1
fi
exit 0
