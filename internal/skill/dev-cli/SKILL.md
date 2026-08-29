---
name: dev-cli
description: 'Manage repositories and work-in-progress with the dev CLI: bootstrap and organise repos safely, own worktree/task lifecycle, safely prepare/finalize/retire agent sessions, run guarded Git transactions, capture sidecar repo notes, track HOT/WARM/COLD tasks, navigate via TUI, and bridge gh/glab/Azure DevOps/herdr/tmux/zellij. Use when starting, parking, resuming or retiring work; preserving agent transcripts; scanning or organising repos; capturing/searching repo thoughts; choosing worktree isolation; or cleaning stale branches, checkouts and sessions.'
---

# dev-cli

The `dev` command is a thin glue layer over git, worktrees, forges and agent
runtimes. It exists to stop four different things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux / zellij
                a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
human/task facts such as identity, checkout mode, **state**, **owner** and **next
action** in one TOML file per task under configured `paths.state_dir/tasks`
(default `$XDG_DATA_HOME/dev/tasks`); transient pane data is not persisted.

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

Only **git** is required. `herdr`, `tmux`, `zellij`, `gh`, `glab` and Azure CLI
each enable more and degrade cleanly when absent — never make a step
hard-depend on one.

## Worktree ownership — read this before creating any worktree

There are three mechanisms that create git worktrees, and picking the wrong one
is the most common source of confusion. The rule:

| Kind of worktree | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff — anything you might return to | **`dev`** | `paths.worktree_path`, default `~/Worktrees/<repo>/<branch-slug>` | until `dev done` or `dev sweep` |
| Harness-owned turn-scoped isolation (`isolation: worktree`, `/batch`, `EnterWorktree`) | **Claude Code** | `.claude/worktrees/` (keep gitignored) | owned by that harness; not a transcript-relocation guarantee |
| `herdr worktree create` | **not used** — `dev` runs `git worktree add` itself, then `herdr worktree open --path …` | — | — |

**Rule of thumb: if code/history/plan must remain reviewable or a human may
return tomorrow, use `dev`. Use native isolation only when the harness owns the
entire turn-scoped lifecycle.**

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
| 🌤 `warm` | worktree + branch kept | normally closed | back within days |
| ❄️ `cold` | committed and pushed; worktree removed | nothing | paused, reconstructible anywhere |
| ✅ `done` | merged | may remain open | MERGED; external retirement pending |

```bash
dev repo open api                                  # ad hoc; no task/worktree
dev start api --task "typo" --direct                # track current/main directly
dev start api --task "small" --branch-only --base main
dev start api --task "token refresh" --base main   # isolated worktree → hot
dev park --next "add the regression test" --wip    # → warm, session closed
dev park --cold --push                             # → cold, worktree removed
dev resume "token refresh"                         # → hot, rebuilt if needed
dev prepare --session claude:<uuid>                # arm final transcript handoff
dev done                                           # TTY wizard: inspect dirty state, then FF/PR/merged
dev done --ff                                      # → done/MERGED; resources kept
dev retire "token refresh"                         # external-only cleanup → RETIRED
```

**Parking is the move that matters.** `dev park --next "…"` is what makes it
safe to close a session, and the `--next` text is what makes resuming cheap.
Always suggest a `--next` when parking.

Full detail: `references/task-lifecycle.md`. Before integrating or deleting an
agent-owned checkout, read `references/agent-retirement.md`.

## Pick the task's checkout mode

A task does not imply a worktree. Use the lightest explicit boundary:

- `dev repo open <repo>` / REPOS Enter — ad-hoc open, no task at all.
- `dev start … --direct` / REPOS `d` — track current branch (usually main),
  create no branch/worktree. HOT ↔ WARM only; `dev done` needs no merge mode.
- `dev start … --branch-only` — create and switch a branch in the canonical
  checkout. No concurrent branch there.
- `dev start …` / REPOS `s` — default isolated branch + worktree. Use once work
  may be interrupted, experimental, or parallel.

Do not create isolation before it has a job, but do not carry dirty main changes
into a parallel task implicitly either: a worktree starts from committed HEAD.
Checkpoint first when the new task depends on that work.

## Parallel background work

Read `references/parallel-agents.md` before spawning an independent agent while
another remains live. The fail-closed workflow uses `dev start --json`, requires
a newly created first-class Herdr worktree and its exact returned root pane,
then launches an explicit SpecStory/profile command there. Reused, fallback,
non-Herdr, missing or unverified panes are never launch targets.

`dev` guards writer-claiming direct/branch starts and resume when another
recognized Herdr agent occupies the same canonical Git worktree. Every state,
including `idle`, `done`, and `unknown`, is occupied; Herdr resolves the current
pane before excluding it. Pure repo/worktree/TUI open is navigation to the live
owner, not writer authorization. Never add `--allow-shared-checkout` unless
agents already have coordinated disjoint file ownership.

A new external agent does not inherit the parent permission mode, and a resumed
Claude fork does not restore bypass. Inspect the effective SpecStory/provider
command: local wrappers may already be elevated, so never claim they are safe or
append a conflicting mode. Herdr `done` is turn-settled—not cleanup done.

## Everyday commands

```bash
dev                        # interactive dashboard (plain listing when piped)
dev note add "thought"     # append to repo containing cwd
dev note search "query"    # rebuilds FTS index if needed
dev edit                   # open the effective config; generate it first if absent
dev ls                     # what am I working on, everywhere
dev ls --json              # stable machine-readable form (also over ssh)
dev status                 # what is this directory: repo, branch, task, session
dev sweep                  # what has gone stale or drifted, and what to do
dev sweep --apply          # act on it, confirming each change
dev sweep --merged-worktrees  # from main, audit contained tracked/untracked worktrees
dev prepare --session claude:<uuid>  # arm exact post-writer artifact commit
dev artifact list          # finalization handoffs and receipts
dev done --ff              # integrate only; runtime/worktree stay alive
dev retire <task>          # external-only close/wait/remove/reap

dev git uncommit           # soft reset with a per-worktree message receipt
dev git recommit           # reuse that exact message with normal hooks
dev git pull-rebase        # exact stash OID + index-preserving restore
dev git amend-all          # add -A + no-edit amend; hooks remain enabled

dev start                  # context-aware wizard; confirms before creating
dev start -t "token refresh" --base main  # fast managed worktree task

dev wt create feat/auth --base main    # worktree at the configured path
dev wt list                            # every worktree of this repo
dev wt plan                            # what a new worktree would be set up with
dev wt plan --write                    # seed a committed .dev.toml from it
dev wt rm feat/auth                    # remove the checkout; the branch stays

dev repo list --sizes      # repos, recovery topology, owned logical bytes
dev repo list --no-remote  # local Git with no configured remote
dev repo list --local-only # branches lacking a remote-backed upstream
dev repo context [repo]    # agent-ready checkouts, Git, runtime and task state
dev repo clone owner/name  # clone into the right place, via gh or glab
dev repo clone https://dev.azure.com/acme/Platform/_git/api
dev repo sync --all        # fetch + prune, and report what moved
dev fleet list              # aggregate repo/task/runtime state over SSH
dev fleet sync api --push   # publish, then fast-forward safe matching checkouts
dev repo remote [query]     # search configured forge inventories
dev bootstrap ~/code       # recursively inventory an existing machine
dev bootstrap ~/code --index ~/Projects   # plan a non-destructive symlink catalog
dev gitignore              # .gitignore from GitHub templates + the common bits
dev adopt                  # import existing worktrees/sessions/branches as tasks

dev try redis-streams      # dated scratch directory for an experiment
dev tries mark redis-streams --add important  # durable personal metadata
dev tries archive redis-streams               # reversible; does not delete
dev graduate redis-streams --category Infra   # promote it into a real project

dev stats --heatmap        # where the time actually went
dev stats backfill --repo api  # seed one repo; TUI H then b does this in place
dev stats path             # durable XDG data, not cache
dev summary                # current machine-wide agent context
dev journal                # today's agent-ready development journal
dev journal --since 7d --metrics | opencode run "summarize this"
dev cache list             # regenerable forge/fleet/size/gitignore/note-index caches
dev help worktrees         # quick-reference pages; dev help wt also works
```

Complete generated reference: `references/commands.md`.

## Bootstrapping and adopting an existing machine

Read `references/bootstrap.md` before recursively scanning paths, creating a
symlink index, moving physical repositories, or generating layout config. Move
is destructive and must follow its report → review → apply procedure.

There is nothing to migrate for ordinary use. `dev` discovers repositories through
`paths.scan_roots` plus exact `paths.repo_paths` and never moves, renames or deletes anything.

```bash
dev config init     # detects this machine's roots; every value stays editable
dev adopt           # report existing worktrees, sessions and unmerged branches
dev adopt --apply   # record them as tasks (nothing on disk changes)
```

Do not assume the user's layout is `~/Documents/Program`. Run `dev config show`
or `dev repo list` to see what this machine is actually configured for.

## Dashboard and forge inventory

The TUI has TASKS, REPOS, FLEET, TRY and REMOTE views, switched with tab or vim-style
h/l. TRY `n` creates an experiment; `space` opens metadata/lifecycle actions;
`a` includes retained history. Archive is a reversible same-filesystem move,
not deletion or disk reclamation. `?` opens the full key map.

REMOTE queries authenticated `gh` and `glab` plus configured Azure DevOps
organization/project targets lazily and fully paginates every repository the
account can access. A private cache is shown immediately; stale rows stay
searchable while refresh runs. `/` filters provider, owner/name, visibility and
description, with `vis:private` for an exact visibility match. Enter opens a local clone; `c`
confirms before cloning an absent remote. Use `dev repo remote [query] --json`
for the non-interactive form; `--cached` avoids a network query and `--refresh`
forces a complete synchronization.

`dev journal` derives calendar-day reports from Git rather than storing a
second history database. The default current-user report adds existing
session/WakaTime observations, matching task intent and recent dirty-worktree
snapshots, clearly separated from committed history. `auto` expands at most 100
commit details while retaining complete aggregates; `--max-commits 0` is
unlimited. Use `--author <email>` or `--all-authors` for commit-only team views,
and `--json` for a stable structured report. dev generates the context only; it
does not launch the receiving agent.

`dev summary` is the complementary current-state view across this machine. It
combines durable repositories and present active/deprecated Tries with live Git,
worktree, task, runtime, recovery and catalog facts. The default `auto` Markdown
expands active work and leaves quiet projects in a compact index; `--attention`
selects both active work and recovery hazards, while `--json` always emits the
complete selected snapshot. Use journal for time ranges and `repo context` for
one repository.

Azure DevOps inventory is opt-in and cloud-only:

```toml
[[forge.azure_devops]]
organization = "https://dev.azure.com/acme"
project = "Platform"
```

Install Azure CLI's `azure-devops` extension separately. `dev` never installs
the extension, logs in, changes Azure defaults, or stores a PAT.

FLEET calls each configured host's own `dev` over SSH, preserving its XDG paths.
Enter prefers Herdr remote navigation and falls back to an SSH login shell.
`dev fleet sync` is explicit and strict: it only fast-forwards a clean checkout
of the same behind-only branch; dirty/ahead/diverged clones are never rewritten.

REPOS has LIVE, LATEST and asynchronous SIZE. SIZE is logical
checkout+private-Git bytes; shared Git is separate and marked `+S`. Detail also
shows no-remote/local-only/multi-upstream recovery topology. Press space to
expand linked worktrees; children
show their own Git/session/task state and mark harness-owned or untracked
checkouts `(ephemeral)` / `(external)`. Press `y` followed by `y/p/b/s/w` to
copy contextual Markdown, path, branch, runtime/agent sessions, or every
worktree path. `dev repo context [repo]` prints the full Markdown without a TUI.
`[tui.repos]` chooses columns and default sort; `O` cycles sort and `R` reverses
it.

`H` opens the selected repo's heatmap. On an empty panel, `b` backfills only
that repo and redraws; `r` rereads existing stats. Stats live in
`$XDG_DATA_HOME/dev/stats.db` and are durable observations, not cache — use
`dev stats clear` with an explicit scope. `dev cache clear` only removes
regenerable forge/size/gitignore/note-FTS data under `$XDG_CACHE_HOME/dev`.

`e` edits config and returning live-reloads data/tool bindings; `r` reloads
explicitly. Runtime
backend changes require restarting the TUI.

External tools are explicit `[[tui.tools]]` config. Run `dev tui tools` before
recommending a binding; it shows the exact command and whether it is available.
Commands run through `$SHELL` in the selected checkout. For an alias/function
that exists only in shell rc, set `interactive = true`; dev uses `$SHELL -lic`
and evaluates the command after rc loading. Prefer an executable on PATH when
the binding should be portable across machines.

## Repository quick notes

Read `references/notes.md` before adding/searching/editing/deleting notes or
when deciding whether content belongs in dev vs td/beads.

Quick notes are multiple timestamped Markdown files under configured
`paths.state_dir/notes/<catalog-id>/` (default `$XDG_DATA_HOME/dev/notes`); the
catalog ID attaches them to the canonical clone across worktrees, symlinks and
moves. `dev` does not synchronize notes or catalog state, so cross-host
attachment requires syncing both. SQLite FTS at `$XDG_CACHE_HOME/dev/notes.db`
is disposable: clearing it removes no thoughts and the next search rebuilds it.

TUI context keys: `n` quick-adds on TASKS/REPOS; `N` browses with `/` search,
Enter expand, `a` add, `e` editor, and confirmed `d` delete. In TRY, `n` remains
“new Try.” REMOTE needs a local clone.

## Editing configuration

Use `dev edit` (or `dev config edit`) rather than guessing the XDG path. It
opens the file selected by `--config`, generating the machine-detected starter
when absent, and resolves `--editor` → `$VISUAL` → `$EDITOR` → nvim/vim/vi.

## Rules for agents

1. **Do not equate task with worktree.** Ad-hoc opening needs no task; `--direct`
   tracks current/main; `--branch-only` is lightweight isolation; default
   worktree is for interruption/experimentation/parallelism.

2. **Always pass `--base` for branch/worktree tasks.** Without it a new branch starts from the current
   HEAD, so starting a task while standing on `feature/A` silently builds on
   `feature/A`. This is the single most common way to produce a confusing
   history unattended.

3. **Never `--force` a worktree removal on your own.** `dev wt rm` refuses a
   checkout with uncommitted changes; that refusal is the feature. Ask the
   user before overriding it.

4. **Report drift, do not fix it silently.** `dev sweep` without `--apply`
   changes nothing. Show the user its output rather than running `--apply --yes`
   for them. For merged worktree cleanup, run `dev sweep --merged-worktrees`
   from the canonical main checkout, present every candidate and blocker as a
   QA question, and use `--apply --yes` only after the user explicitly approves
   that exact set. Branch deletion remains a separate `--delete-branches` opt-in.

5. **Prefer a checkpoint commit over `git stash`.** A stash is invisible in the
   log, easy to forget, and cannot be pushed — so it can never reach another
   machine. `dev park --wip` makes a `wip:` commit instead.

6. **One writer per branch at a time.** `dev resume` refuses a task owned by
   another host without `--force`. Before overriding, confirm that machine has
   pushed.

7. **Check `dev wt plan` before blaming a worktree.** A worktree that comes up
   broken is nearly always a provisioning gap, not a git problem. The plan
   shows the detected project types, which tools are missing, and exactly what
   would be copied or run. Dependencies arrive by a per-ecosystem strategy —
   `reinstall` (default), `copy`, `link` or `skip` — and dev refuses an unsound
   one rather than producing a broken checkout: a virtualenv cannot be copied,
   because it bakes its own absolute path into `pyvenv.cfg`.

8. **Do not create a worktree per agent.** Worktrees isolate *change streams*;
   panes isolate *agents*. A writer claim in one checkout needs explicit
   disjoint ownership; pure open/focus may navigate to the existing owner.

9. **Parallel launch is exact-pane and fail-closed.** Accept only a new
   first-class Herdr worktree/root pane from `dev start --json`. Never infer from
   focus/sidebar order, relocate SpecStory with mid-session `EnterWorktree`, or
   treat any recognized agent state as a free pane. Never add
   `--allow-shared-checkout` without coordinated ownership.

10. **Launcher profile and effective permissions are explicit.** Standard
    Claude/Codex put the complete native command inside SpecStory `-c`; verified
    `*-copilot-once` wrappers already wrap SpecStory but inherit effective
    provider permissions, which may be dangerous. Never append a conflicting
    safer mode to an elevated command. Read `parallel-agents.md`.

11. **Cleanup is external, never self-triggered by agent state.** Herdr `done`
    means a turn settled. Arm and finalize exact artifacts first; `dev done`
    integrates only, and `dev retire` must run outside the target workspace.
    Never run raw `git worktree remove --force` against the caller's checkout.

12. **Prefer `dev bootstrap --index` over `--move`.** If the problem is
   navigation, a symlink catalog solves it without changing the authoritative
   paths. For a physical move, never add `--apply --yes` on the user's behalf;
   blocked rows are preconditions to resolve, not checks to bypass.

13. **`dev adopt` without `--apply` changes nothing.** Show the user its report
   rather than applying it for them — which branches count as work in flight is
   their judgement, not yours.

14. **Treat Markdown notes as durable and notes.db as cache.** Never delete
   configured `paths.state_dir/notes` to fix search; use `dev cache clear notes`
   and reindex. Note deletion itself requires explicit confirmation, and `dev`
   does not synchronize note/catalog state between hosts.

15. **Do not call stats.db a cache.** Session/WakaTime observations may not be
   reconstructible. Use `dev stats clear --repo/--source/--all`; use
   `dev cache clear` only for regenerable remote/size/gitignore/note-FTS caches.

16. **Archive is not eviction.** `dev tries archive` is a reversible hidden move
   on the same filesystem; it does not free space. Phase 1 has no project-data
   delete command. Never substitute `rm -rf` merely because a remote exists —
   no-remote, local-only refs, ignored files and stash are independent risks.

17. **Read Git state as counts, not a dirty boolean.** `⇡`/`⇣` are upstream
   divergence; `=` conflicts, `+` staged, `!` unstaged, `?` untracked. Use
   `dev status` or JSON for the unique-path and type breakdown before cleanup.

18. **Commit messages stay English** and follow Conventional Commits, even when
   the conversation is in another language — see the companion `git-workflow`
   skill, which owns commit conventions, SemVer and branch naming. This skill
   does not duplicate them.

## When to use this skill

- "What am I working on?" / "我在做什麼" / too many terminal workspaces open.
- Starting, parking or resuming a piece of work.
- Spawning independent background agents without disturbing a live checkout.
- "Where should this worktree go?" / "should I use `claude --worktree` or herdr?"
- A new worktree is missing `node_modules`, `.venv` or `.env`.
- Cleaning up stale branches, worktrees and sessions.
- Cloning, creating or auditing repositories across a machine.
- Promoting an experiment into a real project.
- "Which repo do I spend my time in?"
- Setting up a `.gitignore`, or a harness worktree showing as untracked.
- Adopting a machine that already has repos, worktrees and sessions.
- Recursively scanning, indexing, or physically reorganising existing repos.
- Configuring a flat repo/worktree layout or generating config for one.

## When NOT to use it

- **Commit messages, SemVer, branch naming, PR-vs-main tiering** — that is the
  `git-workflow` skill. This one links to it rather than restating it.
- **Standalone Herdr pane/agent control** — that is the `herdr` skill. The
  `dev start --json` cross-tool launch workflow remains here.
- A single `git add` + `git commit` needs no tooling at all.

## Reference files

- `references/notes.md` — read before repository quick-note operations or
  choosing the boundary with td/beads.
- `references/bootstrap.md` — read before scanning or organising an existing
  machine; includes the non-destructive index default and move safety gates.
- `references/worktree-ownership.md` — who owns which worktree, and how a
  worktree gets a working environment. Read before creating one.
- `references/task-lifecycle.md` — HOT/WARM/COLD/DONE, when to park, and how
  work moves between machines.
- `references/agent-retirement.md` — prepare/finalize/integrate/retire, caller
  safety, exact transcript handling and orphaned-runtime recovery.
- `references/runtime-herdr.md` — how `dev` and herdr divide responsibility,
  including native worktree grouping, collisions and cleanup semantics.
- `references/parallel-agents.md` — read before launching parallel background
  work; exact JSON/pane validation, SpecStory profiles and permissions.
- `references/commands.md` — the full command reference, generated from the
  binary by `dev skill sync` so it cannot drift.

## Gotchas

- **`dev` may be a shell wrapper around the binary.** The trusted `shell-init`
  output defines a wrapper that gives navigation commands a private child-only
  file descriptor, reads one NUL-terminated path and calls `builtin cd` without
  evaluating command output. Without the wrapper, the binary's printable legacy
  directive cannot change the parent shell — that is not a bug.
- **A worktree is a clean checkout.** It has no `node_modules`, no `.venv` and
  none of the gitignored env files. `dev` provisions it; `--no-provision`
  skips that and leaves you to it.
- **Only explicitly included, gitignored stable regular files are copied.**
  Source swaps and symlinked destination parents are refused; existing targets
  are reported skipped, not copied. Do not universally include
  `.claude/settings.local.json`: only sticky/plain-Claude profiles opt in.
- **Removing a worktree never deletes the branch.** Those are separate
  decisions; conflating them is how work gets lost.
- **Bare `dev done` is interactive only on a TTY.** It reports branch relation
  and dirty-content equivalence, then offers commit/discard and FF/PR/cleanup.
  Non-interactive use remains report-only without `--ff`/`--pr`. Unique discard
  requires `DROP` or explicit `--dirty=discard --yes`.
- **The stats sampler must be scheduled.** `dev stats` is empty until
  `dev stats backfill` runs once and `dev stats sample` runs periodically.
