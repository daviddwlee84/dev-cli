#!/usr/bin/env bash
# End-to-end smoke test: drives the real binary through a full task lifecycle
# in a throwaway HOME, with the runtime backend pinned to "none" so no
# multiplexer is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${BIN:-$ROOT/dev}"

# Always rebuild. The Go build cache makes this nearly free, and testing a
# stale binary against fresh assertions is worse than not testing at all.
echo "building $BIN"
(cd "$ROOT" && go build -o "$BIN" ./cmd/dev)

SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT

export HOME="$SANDBOX/home"
export XDG_DATA_HOME="$HOME/.local/share"
export XDG_CONFIG_HOME="$HOME/.config"
mkdir -p "$HOME/Program"
git config --global user.email dev@example.test
git config --global user.name "dev e2e"
git config --global init.defaultBranch main

# A local "remote" so the push and cold-park paths are exercised for real.
git init --bare --quiet --initial-branch=main "$SANDBOX/origin.git"

cat > "$SANDBOX/config.toml" <<CONFIG
[paths]
scan_roots    = ["$HOME/Program"]
project_root  = "$HOME/Program"
tries_root    = "$HOME/tries"
worktree_root = "$HOME/Worktrees"
state_dir     = "$HOME/state"

[runtime]
backend = "none"

[worktree]
include     = [".env"]
post_create = []
CONFIG

dev() { "$BIN" --config "$SANDBOX/config.toml" "$@"; }
dev_has() {
  local pattern="$1" output
  shift
  output="$(dev "$@")" || return
  grep -q -- "$pattern" <<<"$output"
}

step() { printf '\n\033[1m▸ %s\033[0m\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1" >&2; exit 1; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }

step "doctor"
dev doctor

step "create a repo with a gitignored env file"
REPO="$HOME/Program/demo"
git init --quiet --initial-branch=main "$REPO"
git -C "$REPO" config user.email dev@example.test
git -C "$REPO" config user.name  "dev e2e"
printf '# demo\n'  > "$REPO/README.md"
printf '.env\n'    > "$REPO/.gitignore"
printf 'TOKEN=x\n' > "$REPO/.env"
git -C "$REPO" add README.md .gitignore
git -C "$REPO" commit --quiet -m "chore: initial commit"
git -C "$REPO" remote add origin "$SANDBOX/origin.git"
git -C "$REPO" push --quiet -u origin main
ok "demo repo created and pushed"

step "repo inventory, recovery topology and metadata"
dev_has demo repo list || fail "demo not discovered"
dev_has '"owned_bytes"' repo list --sizes --json || fail "repo size JSON missing"
dev repo mark demo --add important --note "primary fixture" >/dev/null
dev_has '"important"' repo list --json || fail "repo catalog tag missing"

LOCAL="$HOME/Program/local-only"
git init --quiet --initial-branch=main "$LOCAL"
git -C "$LOCAL" config user.email dev@example.test
git -C "$LOCAL" config user.name  "dev e2e"
printf '# local only\n' > "$LOCAL/README.md"
git -C "$LOCAL" add README.md
git -C "$LOCAL" commit --quiet -m "chore: local-only fixture"
dev_has local-only repo list --no-remote || fail "no-remote repo not detected"
dev_has local-only repo list --local-only || fail "local-only branch not detected"
ok "repo discovery, topology, metadata and logical size"

step "start a task"
dev start demo --task auth --branch feat/auth --base main
WT="$HOME/Worktrees/demo/feat-auth"
[[ -d "$WT" ]]            || fail "worktree not created at $WT"
[[ -f "$WT/README.md" ]]  || fail "worktree has no tracked files"
[[ -f "$WT/.env" ]]       || fail "gitignored .env was not provisioned"
ok "worktree created at the templated path and provisioned"

step "ls shows it hot"
dev_has HOT ls || fail "task is not hot"
dev_has '"branch": "feat/auth"' ls --json || fail "json output missing the branch"
ok "inventory reflects the task"

step "commit work on the branch"
git -C "$WT" config user.email dev@example.test
git -C "$WT" config user.name  "dev e2e"
printf 'package auth\n' > "$WT/auth.go"
git -C "$WT" add auth.go
git -C "$WT" commit --quiet -m "feat: add auth"
ok "committed"

step "park warm, with a next action"
dev park feat/auth --next "add the regression test"
dev_has WARM ls                      || fail "task did not go warm"
dev_has "add the regression test" ls  || fail "next action not recorded"
[[ -d "$WT" ]]                              || fail "a warm task must keep its worktree"
ok "parked warm, worktree intact"

step "park cold with a push"
dev park feat/auth --cold --push
dev_has COLD ls  || fail "task did not go cold"
[[ ! -d "$WT" ]]       || fail "a cold task must not keep its worktree"
git -C "$REPO" rev-parse --verify --quiet origin/feat/auth >/dev/null \
  || fail "the branch was not pushed"
ok "cold: worktree gone, work safe on the remote"

step "resume rebuilds the worktree from the branch"
dev resume auth
[[ -f "$WT/auth.go" ]] || fail "resume did not restore the committed work"
dev_has HOT ls   || fail "resumed task is not hot"
ok "rebuilt from origin"

step "done without a mode only reports"
DONE_REPORT="$(dev done auth)"
grep -q "Nothing done" <<<"$DONE_REPORT" || fail "done should be a no-op without --ff or --pr"
[[ -d "$WT" ]]                           || fail "reporting must not remove the worktree"
ok "reported, changed nothing"

step "done --ff integrates linearly"
dev done auth --ff
git -C "$REPO" cat-file -e main:auth.go || fail "the branch's commit is not on main"
[[ -z "$(git -C "$REPO" log --merges --oneline)" ]] || fail "--ff created a merge commit"
[[ ! -d "$WT" ]] || fail "done should remove the worktree"
git -C "$REPO" show-ref --verify --quiet refs/heads/feat/auth \
  || fail "the branch should survive without --delete-branch"
ok "fast-forwarded, linear history, branch kept"

step "sweep offers to reap the finished task"
dev_has reap sweep || fail "sweep did not offer to reap the done task"
ok "sweep reports"

step "direct task on main"
dev_has '(direct)' start demo --task "quick main fix" --direct \
  || fail "direct mode was not reported"
[[ "$(git -C "$REPO" branch --show-current)" == "main" ]] \
  || fail "direct mode changed branch"
[[ "$(git -C "$REPO" worktree list --porcelain | grep -c '^worktree ')" == "1" ]] \
  || fail "direct mode created a worktree"
printf 'quick\n' > "$REPO/quick.txt"
git -C "$REPO" add quick.txt
git -C "$REPO" commit --quiet -m "fix: quick main change"
dev_has 'completed directly on main' done "quick main fix" \
  || fail "direct task required fake integration"
ok "direct task stayed on main and finished without a worktree"

step "try and graduate"
dev try redis-streams >/dev/null
TRY="$(find "$HOME/tries" -maxdepth 1 -mindepth 1 -type d | head -1)"
[[ -n "$TRY" ]] || fail "try directory not created"
printf 'notes\n' > "$TRY/NOTES.md"
dev graduate "$(basename "$TRY")" --category Infra >/dev/null
GRAD="$HOME/Program/Infra/redis-streams"
[[ -d "$GRAD" ]]           || fail "graduated project not at $GRAD"
[[ -f "$GRAD/NOTES.md" ]]  || fail "graduate lost the experiment's files"
[[ ! -d "$TRY" ]]          || fail "the try should have been moved, not copied"
git -C "$GRAD" rev-parse HEAD >/dev/null || fail "graduated project has no commit"
ok "experiment promoted with its history"

step "Try metadata, reversible archive and restore"
dev try archive-sample --no-git >/dev/null
ARCHIVE_TRY="$(find "$HOME/tries" -maxdepth 1 -mindepth 1 -type d -name '*-archive-sample' | head -1)"
[[ -n "$ARCHIVE_TRY" ]] || fail "archive sample Try not created"
printf 'reclaim candidate\n' > "$ARCHIVE_TRY/NOTES.md"
dev tries mark archive-sample --add important --note "revisit later" >/dev/null
dev tries deprecate archive-sample >/dev/null
dev_has '"owned_bytes"' tries list --all --sizes --json \
  || fail "Try logical size missing"
dev tries archive archive-sample >/dev/null
[[ ! -d "$ARCHIVE_TRY" ]] || fail "archive left the visible Try in place"
[[ -n "$(find "$HOME/tries/.dev/archive" -type f -name NOTES.md -print -quit)" ]] \
  || fail "archive did not preserve Try data"
dev tries restore archive-sample >/dev/null
[[ -f "$ARCHIVE_TRY/NOTES.md" ]] || fail "restore did not return Try data"
dev tries reactivate archive-sample >/dev/null
dev_has '"important"' tries list --json || fail "Try metadata did not survive lifecycle"
ok "Try identity and data survived deprecate/archive/restore"

step "stats"
dev stats backfill --since 1mo >/dev/null
dev_has demo stats --since 1mo || fail "stats did not record the demo repo"
ok "activity recorded"

step "skill install"
dev skill install --no-link >/dev/null
[[ -f "$HOME/.agents/skills/dev-cli/SKILL.md" ]] || fail "skill not installed"
[[ -f "$HOME/.agents/skills/dev-cli/references/worktree-ownership.md" ]] \
  || fail "skill references not installed"
ok "skill installed"

step "gitignore"
cd "$REPO"
dev gitignore go --offline >/dev/null
grep -q '.claude/worktrees/' "$REPO/.gitignore" || fail "harness section missing"
grep -q '\*.exe' "$REPO/.gitignore"             || fail "language section missing"
printf '# hand written\nmy-rule/\n' >> "$REPO/.gitignore"
dev gitignore python --offline >/dev/null
grep -q 'my-rule/' "$REPO/.gitignore" || fail "regeneration lost a hand-written rule"
[[ "$(grep -c '>>> dev gitignore >>>' "$REPO/.gitignore")" == "1" ]] || fail "markers duplicated"
cd - >/dev/null
ok "gitignore composed, and re-run preserved hand-written rules"

step "adopt"
# A branch genuinely ahead of main: that is what "work in flight" means.
git -C "$REPO" switch --quiet -c adopt-me
printf 'half done\n' > "$REPO/wip.txt"
git -C "$REPO" add wip.txt
git -C "$REPO" commit --quiet -m "feat: unmerged work"
git -C "$REPO" switch --quiet main

dev_has adopt-me adopt || fail "adopt did not find the unmerged branch"
if dev_has adopt-me ls; then
  fail "adopt without --apply must create nothing"
fi
dev adopt --apply --yes >/dev/null
dev_has adopt-me ls || fail "adopt --apply did not record the task"
# Adopting twice must not duplicate.
if dev_has adopt-me adopt; then
  fail "an already-tracked branch should not be offered again"
fi
ok "adopt reported, applied once, and stayed idempotent"

step "config init detects the sandbox layout"
dev_has '\[paths\]' config init --stdout || fail "generated config looks wrong"
ok "config generated"

step "bootstrap a non-destructive repo index"
INDEX="$HOME/repo-index"
INDEX_CONFIG="$HOME/indexed.toml"
dev bootstrap "$HOME/Program" --index "$INDEX" --apply \
  --config-out "$INDEX_CONFIG" >/dev/null
[[ -L "$INDEX/demo" ]] || fail "bootstrap did not create the repo symlink"
[[ -d "$REPO" ]]       || fail "bootstrap index moved the physical repo"
# The generated config puts the index first but keeps new repos on the
# physical project root.
grep -q "repo-index" "$INDEX_CONFIG" || fail "generated config does not scan the index"
grep -q 'project_root = "~/Program"' "$INDEX_CONFIG" \
  || fail "generated config would create physical repos inside the index"
INDEX_LIST="$("$BIN" --config "$INDEX_CONFIG" repo list --long)"
grep -q '~/repo-index/demo' <<<"$INDEX_LIST" \
  || fail "normal repo discovery does not use the symlink index"
ok "recursive scan + symlink index, physical layout untouched"

printf '\n\033[32m all end-to-end checks passed\033[0m\n'
