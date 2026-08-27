#!/usr/bin/env bash
# End-to-end smoke test: drives the real binary through a full task lifecycle
# in a throwaway HOME, with the runtime backend pinned to "none" so no
# multiplexer is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${BIN:-$ROOT/dev}"

if [[ ! -x "$BIN" ]]; then
  echo "building $BIN"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/dev)
fi

SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT

export HOME="$SANDBOX/home"
export XDG_DATA_HOME="$HOME/.local/share"
export XDG_CONFIG_HOME="$HOME/.config"
mkdir -p "$HOME/Program"

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

step "repo list"
dev repo list | grep -q demo || fail "demo not discovered"
ok "repo discovered"

step "start a task"
dev start demo --task auth --branch feat/auth --base main
WT="$HOME/Worktrees/demo/feat-auth"
[[ -d "$WT" ]]            || fail "worktree not created at $WT"
[[ -f "$WT/README.md" ]]  || fail "worktree has no tracked files"
[[ -f "$WT/.env" ]]       || fail "gitignored .env was not provisioned"
ok "worktree created at the templated path and provisioned"

step "ls shows it hot"
dev ls | grep -q HOT || fail "task is not hot"
dev ls --json | grep -q '"branch": "feat/auth"' || fail "json output missing the branch"
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
dev ls | grep -q WARM                       || fail "task did not go warm"
dev ls | grep -q "add the regression test"  || fail "next action not recorded"
[[ -d "$WT" ]]                              || fail "a warm task must keep its worktree"
ok "parked warm, worktree intact"

step "park cold with a push"
dev park feat/auth --cold --push
dev ls | grep -q COLD  || fail "task did not go cold"
[[ ! -d "$WT" ]]       || fail "a cold task must not keep its worktree"
git -C "$REPO" rev-parse --verify --quiet origin/feat/auth >/dev/null \
  || fail "the branch was not pushed"
ok "cold: worktree gone, work safe on the remote"

step "resume rebuilds the worktree from the branch"
dev resume auth
[[ -f "$WT/auth.go" ]] || fail "resume did not restore the committed work"
dev ls | grep -q HOT   || fail "resumed task is not hot"
ok "rebuilt from origin"

step "done without a mode only reports"
dev done auth | grep -q "Nothing done" || fail "done should be a no-op without --ff or --pr"
[[ -d "$WT" ]]                         || fail "reporting must not remove the worktree"
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
dev sweep | grep -q reap || fail "sweep did not offer to reap the done task"
ok "sweep reports"

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

step "stats"
dev stats backfill --since 1mo >/dev/null
dev stats --since 1mo | grep -q demo || fail "stats did not record the demo repo"
ok "activity recorded"

step "skill install"
dev skill install --no-link >/dev/null
[[ -f "$HOME/.agents/skills/dev/SKILL.md" ]] || fail "skill not installed"
[[ -f "$HOME/.agents/skills/dev/references/worktree-ownership.md" ]] \
  || fail "skill references not installed"
ok "skill installed"

printf '\n\033[32m all end-to-end checks passed\033[0m\n'
