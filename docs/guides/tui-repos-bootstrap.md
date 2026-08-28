---
description: Navigate tasks, repositories, experiments, and remotes in the TUI; inventory or adopt existing work without destructive migration.
authority: project
status: evolving
verified_on: 2026-08-28
---

# TUI, repositories, and bootstrap

Bare `dev` opens an interactive dashboard when standard input/output are terminals. When piped, it prints the plain task listing so shell composition remains predictable.

## Four views

| View | Question | Source |
|---|---|---|
| TASKS | What am I working on? | task registry plus live Git/runtime facts |
| REPOS | What durable repositories exist here? | configured scan roots and local catalog |
| TRY | Which experiments can I resume, archive, or graduate? | experiment catalog plus live facts |
| REMOTE | What can I open or clone? | authenticated `gh`/`glab` inventories and cache |

Switch with `tab`, `h`/`l`, or arrows. Use `j`/`k`, `g`/`G`, `ctrl+d`/`ctrl+u`, `/` to filter, `?` for help, and `esc`/`q` to leave the current mode.

## Common actions

### TASKS

```text
enter/o   open the selected task
p         park warm and enter the next action
c         edit the next action
1/2/3     HOT/WARM/COLD filters
```

A COLD task must be rebuilt with `dev resume`; the TUI does not silently recreate it through a generic open action.

### REPOS

```text
enter/o   ad-hoc open without creating a task
space     expand linked worktrees
m         edit repository tags/note
d         track direct work on the current branch
s         start isolated work: branch + worktree + provisioning + runtime
H         open the repository activity heatmap
y         open copy/context actions
```

Expanded rows explain every linked worktree, including harness-owned `(ephemeral)` and otherwise unmanaged `(external)` checkouts. The LIVE column shows runtime activity separately from task state.

`dev repo context [repo]` emits the same agent-ready Markdown context available from the TUI copy menu, including paths, Git/worktree/runtime facts, and tasks.

### TRY and REMOTE

TRY handles low-cost experiments, reversible archive/restore, marking, and graduation. Archive is organization, not deletion or disk reclamation.

REMOTE loads lazily so startup does not wait for the network. Enter opens a local clone; `c` confirms before cloning an absent repository; `r` refreshes forge inventories and their private XDG cache.

## External tools

```bash
dev tui tools
```

Configured tools run through `$SHELL` in the selected checkout while the alternate screen is suspended. `interactive = true` uses `$SHELL -lic` so local aliases/functions can resolve; use a real executable on `PATH` when the binding must be portable.

```toml
[[tui.tools]]
key = "L"
name = "lazygit"
run = "lazygit"
```

Keys are case-sensitive and cannot shadow dashboard-owned bindings. Returning from an editor can reload most config; switching runtime backend requires restarting the TUI.

## Inventory an existing machine

Start with a report:

```bash
dev bootstrap ~/code /mnt/work
dev bootstrap ~/code --json
```

The scanner identifies canonical checkouts, linked worktrees, bare repositories, and symlink aliases, then deduplicates them by Git identity.

The recommended organization layer is a non-destructive symlink index:

```bash
dev bootstrap ~/code --index ~/Projects --layout flat
dev bootstrap ~/code --index ~/Projects --layout flat --apply
```

Physical moves are a separate, stricter mode. A move plan blocks dirty repositories, linked worktrees, live sessions/current working directories, aliases that would break, occupied destinations, and cross-filesystem renames. If any row is blocked, apply moves none.

## Adopt work already in flight

Bootstrap answers **where repositories are**. Adoption answers **which existing branches, worktrees, and sessions are active work**:

```bash
dev adopt
dev adopt --apply
```

Adopt reports by default and only writes task entries after `--apply` plus confirmation. It does not move, rename, or delete checkouts, and it excludes recognized harness-ephemeral worktrees.

## Sources

- [`internal/help/topics/tui.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/tui.md)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)
