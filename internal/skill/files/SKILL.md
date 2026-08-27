---
name: dev-cli
description: Manage work-in-progress across repos with the `dev` CLI — worktree ownership (dev vs Claude Code vs herdr), the HOT/WARM/COLD task lifecycle, parking and resuming work without losing the thread, repo/forge management via gh and glab, dated experiments and graduating them, and per-repo activity stats. Use when starting/parking/resuming a change stream, creating or cleaning up worktrees, deciding where a worktree belongs, listing what is in progress, or when a user's terminal workspaces have piled up.
---

# dev-cli

The `dev` command is a thin glue layer over git, worktrees, forges and agent
runtimes. It exists to stop four different things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux    a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
only what git cannot answer — a task's **state**, **owner** and **next
action** — in one TOML file per task under `$XDG_DATA_HOME/dev/tasks/`.

**The problem it solves:** when a terminal multiplexer's sidebar is the only
record of what you are working on, nothing can ever be closed. `dev` gives that
record a home outside the runtime, so closing a session stops meaning
abandoning a task.

## Check it is there first

`dev` is not installed everywhere. Before using it:

```bash
command -v dev || echo "not installed"
dev doctor          # what works on this machine, and what degrades
```

Only **git** is required. `herdr`, `tmux`, `gh` and `glab` each enable more and
degrade cleanly when absent — never make a step hard-depend on one.

## Worktree ownership — read this before creating any worktree

There are three mechanisms that create git worktrees, and picking the wrong one
is the most common source of confusion. The rule:

| Kind of worktree | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff — anything you might return to | **`dev`** | `paths.worktree_path`, default `~/Worktrees/<repo>/<branch-slug>` | until `dev done` or `dev sweep` |
| Turn-scoped subagent isolation (`isolation: worktree`, `/batch`, `EnterWorktree`) | **Claude Code** | `.claude/worktrees/` (keep gitignored) | dies with the turn |
| `herdr worktree create` | **not used** — `dev` runs `git worktree add` itself, then `herdr worktree open --path …` | — | — |

**Rule of thumb: might a human come back to it tomorrow → `dev`. Does it die
with this agent turn → Claude Code native.**

Why `dev` does not delegate to `herdr worktree create`: the path policy has to
hold on machines where herdr is not installed. `dev` creates the checkout with
plain git and asks herdr only to *open* it — which still makes it appear in the
sidebar grouped under its parent repo with its own branch and ahead/behind row.

Never nest a `dev` worktree inside a repository. A checkout inside another
checkout makes every indexer, file watcher and `rg` run see a second copy of
the tree. `dev` refuses to do it.

Full detail: `references/worktree-ownership.md`.

## The task lifecycle

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
```

**Parking is the move that matters.** `dev park --next "…"` is what makes it
safe to close a session, and the `--next` text is what makes resuming cheap.
Always suggest a `--next` when parking.

Full detail: `references/task-lifecycle.md`.

## Everyday commands

```bash
dev                        # interactive dashboard (plain listing when piped)
dev ls                     # what am I working on, everywhere
dev ls --json              # stable machine-readable form (also over ssh)
dev status                 # what is this directory: repo, branch, task, session
dev sweep                  # what has gone stale or drifted, and what to do
dev sweep --apply          # act on it, confirming each change

dev wt create feat/auth --base main    # worktree at the configured path
dev wt list                            # every worktree of this repo
dev wt rm feat/auth                    # remove the checkout; the branch stays

dev repo list              # every repo under the scan roots
dev repo clone owner/name  # clone into the right place, via gh or glab
dev repo sync --all        # fetch + prune, and report what moved
dev gitignore              # .gitignore from GitHub templates + the common bits
dev adopt                  # import existing worktrees/sessions/branches as tasks

dev try redis-streams      # dated scratch directory for an experiment
dev graduate redis-streams --category Infra   # promote it into a real project

dev stats --heatmap        # where the time actually went
dev help worktrees         # quick-reference pages
```

Complete generated reference: `references/commands.md`.

## Adopting an existing machine

There is nothing to migrate. `dev` discovers repositories through
`paths.scan_roots` and never moves, renames or deletes anything.

```bash
dev config init     # detects this machine's roots; every value stays editable
dev adopt           # report existing worktrees, sessions and unmerged branches
dev adopt --apply   # record them as tasks (nothing on disk changes)
```

Do not assume the user's layout is `~/Documents/Program`. Run `dev config show`
or `dev repo list` to see what this machine is actually configured for.

## Rules for agents

1. **Always pass `--base`.** Without it a new branch starts from the current
   HEAD, so starting a task while standing on `feature/A` silently builds on
   `feature/A`. This is the single most common way to produce a confusing
   history unattended.

2. **Never `--force` a worktree removal on your own.** `dev wt rm` refuses a
   checkout with uncommitted changes; that refusal is the feature. Ask the
   user before overriding it.

3. **Report drift, do not fix it silently.** `dev sweep` without `--apply`
   changes nothing. Show the user its output rather than running `--apply --yes`
   for them.

4. **Prefer a checkpoint commit over `git stash`.** A stash is invisible in the
   log, easy to forget, and cannot be pushed — so it can never reach another
   machine. `dev park --wip` makes a `wip:` commit instead.

5. **One writer per branch at a time.** `dev resume` refuses a task owned by
   another host without `--force`. Before overriding, confirm that machine has
   pushed.

6. **Do not create a worktree per agent.** Worktrees isolate *change streams*;
   panes isolate *agents*. Several agents working on disjoint files of one
   feature belong in one checkout.

7. **`dev adopt` without `--apply` changes nothing.** Show the user its report
   rather than applying it for them — which branches count as work in flight is
   their judgement, not yours.

8. **Commit messages stay English** and follow Conventional Commits, even when
   the conversation is in another language — see the companion `git-workflow`
   skill, which owns commit conventions, SemVer and branch naming. This skill
   does not duplicate them.

## When to use this skill

- "What am I working on?" / "我在做什麼" / too many terminal workspaces open.
- Starting, parking or resuming a piece of work.
- "Where should this worktree go?" / "should I use `claude --worktree` or herdr?"
- A new worktree is missing `node_modules`, `.venv` or `.env`.
- Cleaning up stale branches, worktrees and sessions.
- Cloning, creating or auditing repositories across a machine.
- Promoting an experiment into a real project.
- "Which repo do I spend my time in?"
- Setting up a `.gitignore`, or a harness worktree showing as untracked.
- Adopting a machine that already has repos, worktrees and sessions.

## When NOT to use it

- **Commit messages, SemVer, branch naming, PR-vs-main tiering** — that is the
  `git-workflow` skill. This one links to it rather than restating it.
- **Driving herdr panes and agents directly** — that is the `herdr` skill.
- A single `git add` + `git commit` needs no tooling at all.

## Reference files

- `references/worktree-ownership.md` — who owns which worktree, and how a
  worktree gets a working environment. Read before creating one.
- `references/task-lifecycle.md` — HOT/WARM/COLD/DONE, when to park, and how
  work moves between machines.
- `references/runtime-herdr.md` — how `dev` and herdr divide responsibility,
  and the sidebar configuration that makes task state visible.
- `references/commands.md` — the full command reference, generated from the
  binary by `dev skill sync` so it cannot drift.

## Gotchas

- **`dev` is a shell function, not just a binary.** Commands that move you into
  a checkout print a `cd` directive for the wrapper installed by
  `eval "$(dev shell-init zsh)"`. Without it they print a path and do not move
  you — that is not a bug.
- **A worktree is a clean checkout.** It has no `node_modules`, no `.venv` and
  none of the gitignored env files. `dev` provisions it; `--no-provision`
  skips that and leaves you to it.
- **Only gitignored files are copied into a worktree.** Listing a *tracked*
  file in `worktree.include` does nothing, because git already put it there on
  the correct branch — and copying it would overwrite that version.
- **Removing a worktree never deletes the branch.** Those are separate
  decisions; conflating them is how work gets lost.
- **`dev done` defaults to reporting.** With neither `--ff` nor `--pr` it
  explains the options and changes nothing.
- **The stats sampler must be scheduled.** `dev stats` is empty until
  `dev stats backfill` runs once and `dev stats sample` runs periodically.
