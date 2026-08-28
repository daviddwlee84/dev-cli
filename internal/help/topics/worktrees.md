# Worktrees

A worktree is a second working directory for the same repository: its own
files, its own checked-out branch, sharing one history and one remote.

## What they are actually for

**Preventing two writers from editing the same files at once.** Not feature
management — that is what branches are for.

So the question is never "how many agents am I running?" It is:

> Could these two writers touch the same file?

- Different files, one goal → one checkout is fine. Several agents, several
  panes, one branch.
- Different *approaches* to the same problem → one worktree each, so they can
  be compared and the loser discarded.
- Anything where the file ranges are unknown → worktrees.

## Who owns which worktree

| Kind | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, handoff | `dev` | `~/Worktrees/<repo>/<slug>` | until done or swept |
| Harness-owned turn-scoped isolation | Claude Code | `.claude/worktrees/` | owned by that harness; no transcript-relocation guarantee |

**Might you review or return to the code/history/plan later → `dev`.**

Never put a long-lived worktree inside the repository. Every file watcher,
language server, indexer and `rg` run in the outer repo then sees a second copy
of the whole tree.

## Commands

```bash
dev wt create feat/auth --base main   # create at the configured path
dev wt list                           # every worktree of this repo
dev wt open feat/auth                 # open an existing one
dev wt rm feat/auth                   # remove the checkout; branch survives
dev wt provision                      # re-run setup on an existing checkout
```

## Always pass a base

```bash
dev wt create fix/orderbook --base main
```

Without `--base`, a new branch starts from the **current HEAD**. Create a
worktree while standing on `feature/A` and `fix/orderbook` quietly inherits
`feature/A`'s commits. This is the single most common way to end up with a
history nobody intended.

## A worktree starts empty of everything untracked

No `node_modules`, no `.venv`, no `.env`. `dev` fixes this on create:

- copies the gitignored files listed in `worktree.include`;
- symlinks anything in `worktree.link` (opt-in);
- runs `post_create`, detected from lockfiles when set to `"auto"`.

Only files that are **both listed and gitignored** are copied. A tracked file
is already in the checkout on the correct branch — copying the other branch's
version over it would be wrong.

## Removing one

```bash
dev wt rm feat/auth
```

Never deletes the branch. A dirty checkout needs an explicit `--force`, and
that refusal is the feature, not an obstacle.

If the directory was deleted behind git's back, `git worktree prune` clears the
stale entry — `dev wt rm` and `dev sweep` do this for you.
