# dev-cli — a thin glue layer over Git, worktrees, forges, and agent runtimes

## Context

This machine currently has **31 open Herdr workspaces**, 39 repos under
`~/Documents/Program`, and 19 experiments in `~/src/tries`. The Herdr sidebar is
being used as a *memory of what I'm working on*, so nothing ever gets closed —
Herdr is forced to be both "live runtime" and "WIP inventory" at once. Meanwhile
worktrees are confusing: Claude Code has a native `--worktree`, Herdr has
`herdr worktree create`, Worktrunk exists, and it's unclear who owns what or how
a new worktree gets a working environment (`uv sync`, `npm ci`, `.env`).

`dev` fixes this by being **glue, not a second Git database**:

```
Git remote      durable code state       (source of truth)
git worktree    disposable local checkout
Herdr / tmux    per-host live runtime
dev             human intent + inventory + navigation + one worktree policy
```

Everything derivable from Git or the runtime is **derived live**. `dev` persists
only what Git cannot answer: `state`, `next`, `owner`, `note`.

Outcome: closing a Herdr workspace stops feeling like abandoning a task, so the
sidebar can shrink back to an actual working set.

---

## 1. Doctrine the tool encodes

### 1.1 Task state machine

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | worktree + branch | Herdr space open | working on it now |
| 🌤 `warm` | worktree + branch kept | space **closed** | back within days |
| ❄️ `cold` | committed + pushed; worktree removed | nothing | paused; reconstructible anywhere |
| ✅ `done` | merged | nothing | branch + worktree + entry deleted |

`dev park` = warm/cold transition. **Closing a space ≠ abandoning a task.**

### 1.2 Isolation boundary

> **Worktree per change stream, pane per agent.** Not one worktree per agent.

### 1.3 Worktree ownership — the confusion-killer

This is the question that has been unclear; `dev` takes one explicit stance and
the skill teaches agents to respect it.

| Worktree kind | Owner | Location | Lifetime |
|---|---|---|---|
| Feature / bugfix / experiment / cross-machine handoff | **`dev`** | `worktree.path_template` (default `~/Worktrees/<repo>/<slug>`) | until `dev done` / `dev sweep` |
| Claude Code subagent `isolation: worktree`, `/batch`, `EnterWorktree` | **Claude Code** | `.claude/worktrees/` (gitignored) | dies with the turn |
| `herdr worktree create` | **not used directly** — `dev` runs `git worktree add` itself, then `herdr worktree open --path …` so path policy lives in exactly one place | — | — |
| Worktrunk | **not adopted** — `dev wt` fills that role | — | — |

Rule of thumb: **might I come back tomorrow → `dev`; dies with this agent turn →
Claude Code native.** Never nest a `dev` worktree inside another `dev` worktree.

Herdr visibility is preserved: after `git worktree add`, `dev` calls
`herdr worktree open --path <path> --label <task>`, which registers the checkout
as a Herdr workspace grouped under the parent repo's space with its own
branch/ahead/behind row. `dev` additionally pushes
`herdr workspace report-metadata --source dev --token stage=HOT --token next="…"`
so the sidebar shows the task state (config already documents `$name` tokens
require a matching `rows` entry in `~/.config/herdr/config.toml`).

### 1.4 Worktree provisioning

A fresh checkout has no `node_modules`, no `.venv`, no `.env`. `dev wt create`
runs a provisioning step (skippable with `--no-provision`):

1. **Copy gitignored files** listed in `worktree.include` — same semantics as
   `.worktreeinclude` in the existing `git-workflow` skill (copies only files
   that are *both* matched and gitignored).
2. **Optional symlinks** for heavy dirs (`worktree.link`, default empty —
   opt-in, since sharing `node_modules` across checkouts can break native deps).
3. **Post-create commands**: `worktree.post_create = "auto"` detects
   `uv.lock`→`uv sync`, `poetry.lock`→`poetry install`, `pnpm-lock.yaml`→`pnpm i
   --frozen-lockfile`, `package-lock.json`→`npm ci`, `yarn.lock`→`yarn install
   --immutable`, `go.mod`→`go mod download`, `Cargo.toml`→`cargo fetch`;
   otherwise an explicit list. Per-repo override in `<repo>/.dev.toml`.

---

## 2. Architecture

Go 1.26 (installed), pure-Go deps only (no cgo): `spf13/cobra`,
`charmbracelet/bubbletea` + `bubbles` + `lipgloss` + `glamour`,
`BurntSushi/toml`, `modernc.org/sqlite`.

```
cmd/dev/main.go
internal/config/     TOML load/merge, path templates, XDG resolution
internal/gitx/       porcelain wrappers: worktree list, status, ahead/behind,
                     branch classification, wip-commit
internal/repo/       scan-root discovery + cache
internal/wt/         create/open/remove + provisioning (§1.4)
internal/task/       one-TOML-per-task registry
internal/runtime/    Runtime interface + herdr / tmux / none adapters
internal/forge/      gh + glab adapters (both optional)
internal/stats/      sampler, SQLite store, heatmap renderer, WakaTime importer
internal/tui/        bubbletea dashboard
internal/help/       go:embed'd git-workflow quick pages
internal/skill/      go:embed'd SKILL.md + install/sync
skill/               SKILL.md + references/ (source of truth, embedded at build)
docs/                mkdocs-able reference
```

### Config — `$XDG_CONFIG_HOME/dev/config.toml`

Every path is configurable (per the requirement that `~/Worktrees` may need to
live on `/mnt`, or be lowercase):

```toml
[paths]
scan_roots    = ["~/Documents/Program", "~/src/tries", "~/.local/share/chezmoi"]
tries_root    = "~/src/tries"
worktree_root = "~/Worktrees"                                   # override freely
worktree_path = "{{worktree_root}}/{{repo}}/{{branch|slug}}"    # full template
state_dir     = "~/.local/share/dev"

[runtime]
backend = "auto"          # auto | herdr | tmux | none

[worktree]
include     = [".env", ".env.local"]
link        = []
post_create = "auto"

[stats]
sampler  = true
wakatime = false          # dev stats --import-wakatime reads ~/.wakatime.cfg
```

Template vars: `{{worktree_root}} {{repo}} {{repo_path}} {{branch}} {{category}}
{{host}} {{date}}`, filter `|slug` (`feat/auth/x` → `feat-auth-x`).

### State — `$XDG_DATA_HOME/dev/`

`tasks/<repo>__<branch-slug>.toml`, **one file per task** so a git-synced
`state_dir` doesn't conflict across machines:

```toml
repo = "atp-sipui"; repo_path = "~/Documents/Program/atp-sipui"
branch = "fix/gx-security-recovery"; base = "main"
worktree_path = "~/Worktrees/atp-sipui/fix-gx-security-recovery"
state = "warm"; owner = "jingle-235"
next = "finish refresh regression test"
agent_session = "claude:2136a917-…"
created = 2026-08-21; updated = 2026-08-27
```

Plus `stats.db` (SQLite) and `cache/repos.json`. If `state_dir` is a git repo,
`dev sync` pulls/pushes it — the cross-machine story, no Herdr sync needed.

### Runtime adapter

```go
type Runtime interface {
    Name() string
    Available() bool
    Open(ctx, dir, label string) (handle string, err error)
    Close(ctx, handle string) error
    List(ctx) ([]Session, error)
    Annotate(ctx, handle string, kv map[string]string) error  // no-op for tmux
}
```

- **herdr**: shells out and parses the JSON already emitted by
  `herdr workspace list` / `agent list` / `worktree list`; uses
  `worktree open --path`, `workspace close`, `workspace report-metadata`.
- **tmux**: `new-session -d -c <dir> -s <label>`.
- **none**: prints the path for the shell wrapper to `cd`.

### Shell integration (required — a child process cannot `cd` its parent)

`eval "$(dev shell-init zsh)"` installs a `dev()` wrapper that evals `cd`
directives on stdout, exactly like the `try` wrapper already in this shell.

---

## 3. Command surface

```
dev                          # TUI if tty, else `dev ls`
dev ls [--json] [--state hot|warm|cold] [--repo R]
dev status                   # context for cwd: repo, branch, task, worktree, runtime
dev start <repo> [--task N] [--branch B] [--base REF] [--no-worktree]
dev park [--next "…"] [--wip] [--state warm|cold]
dev resume <task>
dev done [--ff | --pr] [--keep-branch]
dev sweep [--apply]          # stale-candidate report; never auto-deletes
dev wt list|create|open|rm|provision
dev repo list|clone|open|sync|new     # gh / glab backed
dev try [name]               # dated scratch dir in tries_root
dev graduate [try] --category <Cat> [--private] [--push]
dev stats [--heatmap] [--by repo|branch] [--since 12m] [--import-wakatime]
dev help [topic]             # git-workflow quick pages
dev skill install|sync|print ;  dev --skill
dev doctor ;  dev completion <shell> ;  dev shell-init <shell> ;  dev sync
```

`dev ls` output shape:

```
TASK                     STATE  REPO         BRANCH                    GIT      RUNTIME  NEXT
atp security recovery    HOT    atp-sipui    fix/gx-security-recovery  ↑2 ●     herdr w7 finish refresh regression test
orderbook experiment     WARM   trading      exp/orderbook-v2          clean    —        compare against baseline
settings redesign        COLD   website      feature/settings          pushed   —        —
```

### Safety rails (mirrors Herdr's conservative lifecycle)

- `dev` never runs `git worktree remove --force` without `--force` **and** a
  confirmation; `sweep` reports, `sweep --apply` still prompts per item.
- `dev done` refuses on a dirty tree; branch deletion is a separate, explicit step.
- `park --wip` makes a `wip:` checkpoint commit rather than a long-lived stash
  (stashes are invisible and don't cross machines).

---

## 4. Milestones (all of these are v1 — each independently shippable)

- **M0 — skeleton.** `go mod init`, cobra root, config loader + path templates,
  `doctor`, `completion`, `shell-init`. Gate: `dev doctor` reports git / gh /
  glab / herdr / tmux presence.
- **M1 — inventory + lifecycle.** `internal/gitx`, `internal/repo`,
  `internal/task`, runtime adapters; `ls`, `status`, `start`, `park`, `resume`,
  `sweep`. Gate: 31 Herdr spaces reconcile against `dev ls` output.
- **M2 — worktrees.** `internal/wt` with path template + provisioning + Herdr
  surfacing; `wt *`, `done`. Gate: `dev wt create` produces a working checkout
  (deps installed, `.env` present) that appears grouped in the Herdr sidebar.
- **M3 — repo & forge.** `internal/forge`; `repo list|clone|open|sync|new`,
  `dev sync`. Gate: works with `gh`, with `glab`, and with neither.
- **M4 — try / graduate.** `dev try` (dated dirs, compatible with the existing
  `~/src/tries`), `dev graduate` → move into `Program/<Category>/`, `git init`,
  initial commit, optional `gh repo create --source`, register as a task.
- **M5 — stats.** SQLite store; sampler recording `herdr agent list` + git
  commit/reflog activity; `--import-wakatime`; ASCII heatmap in the
  `Mon/Wed/Fri` + `░▒▓█` style already in use.
- **M6 — TUI.** Bubbletea dashboard: task list, filter by state, inline `next`
  editing, enter → open runtime, `p` park, `s` sweep view, `?` help.
- **M7 — help + skill.** `internal/help` glamour-rendered pages; the skill and
  its self-install (§5).

---

## 5. The agent skill

Lives at `skill/SKILL.md` in this repo, embedded with `go:embed`, so the binary
is the authority for its own skill — the same pattern `herdr --skill` uses and
that `~/.local/share/chezmoi/scripts/lib/herdr_skill.sh` already consumes.

- `dev skill sync` regenerates the command-reference section from the live cobra
  tree, so the skill can never drift from the tool. `--check` exits non-zero on
  drift (wire into CI / a pre-push hook).
- `dev skill install` writes `~/.agents/skills/dev/` and symlinks
  `~/.claude/skills/dev` → it, matching the existing layout on this machine
  (`~/.claude/skills/herdr -> ../../.agents/skills/herdr`).
- `dev --skill` prints it, so a chezmoi script can mirror `sync_herdr_skill()`
  without `dev` being a hard dependency of the dotfiles.

Skill content — **defers rather than duplicates**. Commit conventions, SemVer,
branch naming, and PR-vs-main tiering already live in
`agent-skills/skills/local/git-workflow`; this skill links there and owns only
what's new:

- `references/worktree-ownership.md` — §1.3, the dev vs Claude Code vs Herdr
  matrix, so an agent stops improvising which worktree mechanism to use.
- `references/task-lifecycle.md` — §1.1 HOT/WARM/COLD/DONE and when to park.
- `references/commands.md` — generated command reference.
- `references/runtime-herdr.md` — how `dev` and Herdr divide responsibility.

---

## 6. Verification

**Unit** — `internal/gitx` and `internal/wt` against throwaway repos built by a
test helper (`git init`, commits, branches, worktrees) in `t.TempDir()`;
`internal/config` path-template table tests (`|slug`, `~` expansion, `/mnt`
roots, lowercase roots); `internal/task` round-trip TOML.

**Golden** — `dev ls --json` and `dev sweep` against a fixture state dir.

**Adapter contract** — one shared test suite run against `runtime=none` in CI
and against `herdr`/`tmux` locally, skipping when the binary is absent.

**End-to-end** (`scripts/e2e.sh`, temp HOME + temp scan root):

```bash
dev doctor
dev repo new demo && dev start demo --task auth --branch feat/auth --base main
dev wt list                       # worktree at the templated path, deps provisioned
dev park --next "add token test" && dev ls   # → WARM, runtime closed
dev resume auth && dev done --ff  # → merged, branch+worktree cleaned
dev skill sync --check            # → no drift
```

**Manual, on this machine** — `dev ls` against the 31 live Herdr spaces;
`dev wt create` on a real repo and confirm the grouped workspace + `$stage` /
`$next` tokens appear in the Herdr sidebar; `dev stats --heatmap` after a few
days of sampling.

---

## 7. Deferred (documented in `TODO.md`, not built now)

Multi-host aggregation via `ssh <host> dev ls --json`; a `tv` (Television)
channel and fzf picker; per-branch single-writer ownership enforcement; Raycast
extension; publishing the skill through the `agent-skills` marketplace.
