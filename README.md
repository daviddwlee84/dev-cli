# dev

A thin glue layer over git, worktrees, forges and agent runtimes.

It exists to stop four things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux    a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
only what git cannot answer — a task's **state**, **owner** and **next
action** — as one TOML file per task.

## The problem

When a terminal multiplexer's sidebar is the only record of what you are
working on, nothing can ever be closed: closing a workspace loses the thread.
So the sidebar grows to thirty-odd entries and stops being scannable, which
defeats the point of having one.

The fix is not discipline. It is giving "what am I working on" a home outside
the runtime, so closing a session is just closing a session.

Bare `dev` opens an interactive dashboard when it has a terminal, and prints
the plain listing when piped — so `dev | grep` and `dev > file` behave as
expected.

```
$ dev ls
   TASK                   STATE  REPO       BRANCH                    GIT   AGE  SESSION       NEXT
🔥 token refresh          HOT    atp-sipui  fix/gx-security-recovery  ↑2 ●  2h   herdr:working add the regression test
🌤 orderbook experiment   WARM   trading    exp/orderbook-v2          clean 6d   —             compare against the baseline
❄️ settings redesign      COLD   website    feat/settings             clean 3w   —             pick up after the API lands
```

## Install

```bash
make install     # builds, installs to ~/.local/bin, installs the agent skill
dev config init  # detects this machine's repo roots and writes a config
```

Then add the shell wrapper to your rc file:

```bash
eval "$(dev shell-init zsh)"      # bash and zsh
dev shell-init fish | source      # fish
```

The wrapper is needed because a child process cannot change its parent's
working directory. Commands that move you into a checkout print a `cd`
directive; the wrapper evaluates it.

```bash
dev doctor       # what works on this machine, and what degrades
```

Only **git** is required. `herdr`, `tmux`, `gh` and `glab` each enable more and
degrade cleanly when absent.

## The lifecycle

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | worktree + branch | session open | working on it now |
| 🌤 `warm` | worktree + branch kept | session **closed** | back within days |
| ❄️ `cold` | committed and pushed; worktree removed | nothing | paused, reconstructible anywhere |
| ✅ `done` | merged | nothing | entry survives until swept |

```bash
dev start api --task "token refresh" --base main   # → hot
dev park --next "add the regression test" --wip    # → warm, session closed
dev park --cold --push                             # → cold, worktree removed
dev resume "token refresh"                         # → hot, rebuilt if needed
dev done --ff                                      # → done, integrated
dev sweep                                          # what has gone stale
```

### A task does not have to mean a worktree

Choose the lightest mode that preserves the boundary you need:

```bash
# No task, no branch, no worktree: just open the canonical repo for ad-hoc work.
dev repo open api

# Track a quick change directly on the branch already checked out (usually main).
dev start api --task "fix typo" --direct

# Use a short-lived branch in the canonical checkout, but no linked worktree.
dev start api --task "small feature" --branch-only --base main

# Default: independent branch + worktree, provisioned and runtime-opened.
dev start api --task "token refresh" --base main
```

Direct work can be parked WARM and resumed, but cannot go COLD because the
canonical checkout cannot be removed. `dev done` on a clean direct task needs
no `--ff` or `--pr`: the work is already on its destination branch.

Start direct for one change stream, then create a normal worktree task later
when real parallelism appears. A new worktree starts from committed HEAD; dirty
main changes are deliberately not smuggled into it, so checkpoint first when
the parallel task depends on them.

### The dashboard

Bare `dev` (or `dev tui`) opens three lists, switched with `tab`:

- **TASKS** — the change streams dev is tracking. What am I working on.
- **REPOS** — every repository under the scan roots, with its branch, dirty
  state, worktree count and per-state task tally, sorted so anything in flight
  is at the top. What do I have here.
- **REMOTE** — repositories visible through the authenticated `gh` and `glab`
  CLIs, marked when a local clone exists. What can I open or clone.

TASKS and REPOS come from the same code paths as `dev ls` and `dev repo list`,
so they cannot disagree. The repo list matters on day one: with forty repositories and
no tasks recorded yet, a task-only dashboard would just be empty.

Navigation is vim-style, arrows alongside:

```
j k        move                 ctrl+d ctrl+u   half a page
g G        top / bottom         h l / tab       previous / next view
/          filter as you type   esc             clear, then quit
```

`enter` opens the selected row in the runtime. In TASKS, `p` parks and prompts
for the next action and `c` edits it. In REPOS, `enter` is pure ad-hoc open,
`s` starts an isolated worktree task, and `d` starts a tracked direct task on
the current branch. That makes the branch/worktree cost an explicit choice,
not something every task silently pays.

**External tools are configured, not fixed.** They run through your shell in
the selected row's checkout; the dashboard suspends and redraws when they exit:

```toml
[[tui.tools]]
key  = "L"
name = "lazygit"
run  = "lazygit"

[[tui.tools]]
key  = "V"
name = "nvim"
run  = "nvim ."

[[tui.tools]]
key  = "B"
name = "vibe"
run  = "vibe"
interactive = true     # load + evaluate aliases/functions after shell rc

[[tui.tools]]
key  = "P"
name = "plans here"
run  = "claude-plans-here"
interactive = true
```

`dev config init` writes the defaults out in full rather than leaving them
implicit, and `dev tui tools` shows what is bound here and whether each one is
actually installed — bindings for missing programs are not offered. A tool
cannot take a key the dashboard already uses; dev reports the clash on load.

REMOTE loads lazily, so dashboard startup never waits on the network. A private
15-minute XDG cache makes later switches instant; `r` refreshes both forges.
`/` searches provider, owner/name and description; enter opens a local clone,
and `c` confirms before cloning an absent repo into `project_root`. The same
inventory is available without the full-screen UI via `dev repo remote [query]`;
`--cached` is its instant/offline form.
REPOS has an explicit LIVE column (`herdr:working`, `herdr:idle`, …); its
detail pane includes the workspace handle. `H` opens the selected repo's
one-year activity heatmap and `e` edits config. Returning from the editor, or
pressing `r`, reparses config and reloads data/tool bindings without restarting
the TUI; a runtime-backend change is reported as requiring restart.

See `dev help tui` for the full key map.

Going cold is safe because **the branch is the identity and the directory is a
cache**. `dev park --cold` refuses unless the branch is pushed, and `dev resume`
rebuilds the checkout from `origin/<branch>`. Once that holds, the local
filesystem stops being a graveyard of half-finished worktrees.

## Rich Git state

Every inventory surface uses the same compact, starship-like status:

```text
⇕⇡3⇣2 =1 +4 !2 ?3
```

- `⇡` / `⇣` / `⇕` — ahead, behind, diverged
- `=` — conflicted paths
- `+` — staged paths
- `!` — unstaged paths
- `?` — untracked paths

`dev status` and the TUI detail pane also show the unique changed-path total
and type breakdown (added / modified / deleted / renamed). A path staged and
then modified again is one changed path, while correctly appearing in both the
staged and unstaged categories. JSON output exposes all counts separately.

## Worktree ownership

Three mechanisms create git worktrees. `dev` takes one position so nobody has
to improvise:

| Kind | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff | **`dev`** | `~/Worktrees/<repo>/<slug>` | until `dev done` / `dev sweep` |
| Turn-scoped subagent isolation | **Claude Code** | `.claude/worktrees/` (gitignored) | dies with the turn |
| `herdr worktree create` | **not used** — `dev` runs `git worktree add`, then `herdr worktree open --path …` | — | — |

**Might you come back tomorrow → `dev`. Dies with this agent turn → Claude
Code native.**

`dev` does not delegate placement to herdr because the path policy has to hold
on machines without herdr. It creates the checkout with plain git and asks
herdr only to *open* it — which still surfaces it in the sidebar grouped under
its parent repo with its own branch and ahead/behind row.

`dev` refuses to create a worktree inside a repository: a checkout nested in
another checkout makes every indexer, file watcher and `rg` run see a second
copy of the tree.

### Provisioning

A worktree is a clean checkout — no `node_modules`, no `.venv`, no `.env`.
Without provisioning, every new one starts broken. `dev` copies the gitignored
files you list, optionally symlinks heavy directories, and runs a setup
command detected from the lockfiles:

```toml
[worktree]
include     = [".env", ".env.local"]   # only files that are ALSO gitignored
link        = []                       # opt-in; sharing node_modules is risky
post_create = "auto"                   # uv.lock → uv sync, package-lock.json → npm ci, …
```

**Copy or reinstall?** Reinstalling is always correct but can take minutes;
copying is fast but only sound for dependency trees carrying no absolute paths.
That is a per-ecosystem fact, not a preference, so dev knows it:

```toml
[worktree]
strategy = "reinstall"      # reinstall | copy | link | skip

[worktree.strategies]
node = "copy"               # node_modules copies soundly
```

Asking to copy a virtualenv is refused with the reason — it bakes its own
absolute path into `pyvenv.cfg` and `bin/activate` — and narrowed back to
reinstalling rather than silently producing a broken checkout.

`dev wt plan` shows exactly what a new worktree of a repository would get:
which project types were detected, which tools are missing, and every file and
command involved. `dev wt plan --write` seeds a `<repo>/.dev.toml` from it, so
a project can commit its own setup and every machine provisions the same way.

```
$ dev wt plan
PROJECT  MANAGER  FROM               DEPENDENCIES  TOOL
node     npm      package-lock.json  node_modules  installed
python   uv       uv.lock            .venv         installed

   ACTION    WHAT          WHY
✓  copy      .env          gitignored, so the checkout would not have it
✓  copy-dir  node_modules  npm dependencies, copied instead of reinstalling
✓  run       uv sync       uv.lock detected
```

## Other things it does

```bash
dev repo list                  # every repo under the scan roots, with status
dev repo clone owner/name -c Web   # clone into the right place, via gh or glab
dev repo sync --all            # fetch + prune, and report what moved

dev try redis-streams          # dated scratch directory for an experiment
dev graduate redis-streams -c Infra --remote   # promote it into a real project

dev gitignore                  # .gitignore from GitHub's templates + the rest
dev adopt                      # import existing worktrees/sessions as tasks

dev stats --heatmap            # where the time actually went
dev help worktrees             # quick-reference pages for the workflow
```

`dev stats` draws a contribution-style heatmap from two sources: a sampler
watching live agent sessions (the only way to count time spent reading and
debugging), and git history (which backfills the past and survives losing the
database). WakaTime can be imported alongside for editor time.

```bash
dev stats backfill                          # seed from git history, once
dev stats sample --interval 5m              # from cron, every five minutes
dev stats import-wakatime                   # optional
```

## Bootstrapping an existing machine

There is nothing to migrate for ordinary use: `dev` discovers repositories
wherever `scan_roots` point and never requires a particular physical layout.
When you want a recursive audit or a curated navigation layer, bootstrap is the
explicit path:

```bash
dev bootstrap ~/code /mnt/work                    # recursive report, no changes
dev bootstrap ~/code --json                       # machine-readable inventory
dev bootstrap ~/code --index ~/Projects           # plan a flat symlink catalog
dev bootstrap ~/code --index ~/Projects --apply   # create only the ready links
dev bootstrap ~/old --move ~/Projects             # plan physical moves
```

The scanner classifies canonical checkouts, linked worktrees, bare repositories
and symlink aliases, deduplicating by Git common-directory identity rather than
path.

**A symlink index is the default recommendation.** It gives a flat,
metadata-aware navigation root while every physical repository stays exactly
where it was. Put the index first in `scan_roots` and that alias becomes the UI;
normal discovery follows direct repo symlinks and deduplicates index + physical
paths.

Physical move exists, but refuses dirty repos, linked worktrees anywhere in the
clone, live sessions, a current shell inside, aliases that would break,
occupied destinations and cross-filesystem renames. A plan with any blocked row
moves nothing. `--config-out` writes a fresh config for the resulting root
without silently rewriting the user's current one. See `dev help bootstrap`.

## Adopting work in flight

There is nothing to migrate. `dev` discovers repositories wherever your scan
roots point and **never moves, renames or deletes anything** you already have.
`dev config init` probes the conventional locations — `~/Documents/Program`,
`~/src`, `~/code`, a `GHQ_ROOT`, and so on — counts the repositories in each,
and writes only the ones that exist:

```
$ dev config init
wrote ~/.config/dev/config.toml

Detected:
ROOT                 REPOS  ROLE
~/Documents/Program  40     scan root, new projects land here
~/src                16     scan root
```

Repositories are discovered; *tasks* are not. `dev adopt` finds the work
already in flight — linked worktrees from any tool, live runtime sessions, and
local branches ahead of the default branch — and offers to record it. It skips
branches already merged, and the turn-scoped worktrees an agent harness cleans
up itself:

```bash
dev adopt            # report only
dev adopt --apply    # record as tasks; nothing on disk changes
```

A worktree you already have somewhere else keeps working exactly as it did —
`dev` records the path git reports rather than relocating it. Full detail in
`dev help adopting`.

## .gitignore

```bash
dev gitignore                 # detect the languages from the repo's files
dev gitignore python node     # or say so explicitly
dev gitignore --offline       # cached and bundled templates only
```

Language sections come from [GitHub's templates](https://github.com/github/gitignore),
fetched once and cached. On top of those it adds what no language template
covers: the host platform's junk files, editor state, local env files, and the
directories coding-agent harnesses create — an agent's linked worktree left
untracked makes every `git status` in the main checkout unreadable.

Everything it writes goes inside a delimited block, so re-running updates that
block and leaves rules you added by hand alone.

## Configuration

`$XDG_CONFIG_HOME/dev/config.toml`. Write a commented starter with
`dev config init`; see the effective settings with `dev config show`; open the
actual file in `$VISUAL` / `$EDITOR` with either form:

```bash
dev edit
dev config edit
DEV_EDITOR=unused dev edit --editor "code --wait"   # explicit override
```

If the file does not exist, `edit` generates the machine-detected starter first
rather than opening an empty file. Resolution is `--editor` → `$VISUAL` →
`$EDITOR` → `nvim` → `vim` → `vi`.

Every path is configurable, because the right answer depends on the machine —
a faster volume, a different naming convention:

```toml
[paths]
scan_roots    = ["~/Documents/Program", "~/src/tries"]
worktree_root = "/mnt/fast/worktrees"
worktree_path = "{{worktree_root}}/{{repo|lower}}/{{branch|slug}}"
state_dir     = "~/.local/share/dev"        # point at a git repo to sync it
```

Template variables: `worktree_root`, `repo`, `repo_path`, `branch`, `category`,
`host`, `date`. Filters: `|slug` (`feat/auth/x` → `feat-auth-x`), `|lower`,
`|base`. A typo in a variable name fails at load, not as a directory literally
named `{{rep}}`.

A flat project layout is already first-class — it needs no migration and no
special mode:

```toml
[paths]
scan_roots   = ["~/code"]
project_root = "~/code"       # repo new/clone/graduate land at ~/code/<repo>
worktree_root = "/mnt/fast/wt"
worktree_path = "{{worktree_root}}/{{repo|lower}}--{{branch|slug}}"
```

Omit `--category` and canonical repos stay flat. Categories are metadata the
user may choose, never a directory structure dev imposes.

## Multiple machines

Do not sync worktrees or runtime state. Sync branches, and let the remote be
the handoff boundary:

```bash
dev park --cold --push      # on the machine holding the work
dev resume <task> --fetch   # on the machine picking it up
```

`dev` records an owner host per task and refuses to resume someone else's
without `--force`. Two machines committing to one branch is the reliable way
to produce a conflict here; the ownership check prevents it.

For a cross-machine view, `dev ls --json` is a stable contract:

```bash
ssh jingle-235 dev ls --json | jq '.[] | select(.state=="hot")'
```

## The agent skill

`dev` ships the agent skill that documents it, embedded in the binary — the
same pattern `herdr --skill` uses. A skill vendored separately drifts from the
tool it describes, and an agent reading a stale command list is worse than one
reading none.

```bash
dev skill install    # → ~/.agents/skills/dev-cli, symlinked into ~/.claude/skills
dev --skill          # print it, for a dotfiles installer to sync
dev skill sync       # regenerate the command reference from the command tree
dev skill sync --check   # fail if it has drifted — wire into CI
```

The skill defers to the companion `git-workflow` skill for commit conventions,
SemVer and branch naming rather than restating them. It owns what is new: the
worktree ownership rule, the task lifecycle, and how `dev` and herdr divide
responsibility.

## Development

```bash
make          # fmt, vet, test, build
make test
make e2e      # drives the real binary through a full lifecycle in a sandbox
make skill-check
```

Tests build throwaway repositories under `t.TempDir()` via
`internal/gitx/gittest`, and the runtime adapters share one contract suite that
skips backends not installed on the machine — so the suite is meaningful in CI
(where only the null backend exists) and locally (where herdr and tmux do).
