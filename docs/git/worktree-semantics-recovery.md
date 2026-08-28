---
description: Understand what Git worktrees isolate and share, then add, lock, remove, prune, move, and repair them without losing work.
authority: git-scm
status: official
verified_on: 2026-08-28
---

# Worktree semantics and recovery

A repository has one main worktree and zero or more linked worktrees. Each linked worktree has its own working files, index, and `HEAD`, while sharing the repository's object database and most refs/configuration.

!!! info "Freshness"
    **Authority:** `git-worktree(1)` · **Status:** official Git semantics · **Verified:** 2026-08-28. `dev` adds policy and guardrails but does not change these fundamentals.

## Isolated versus shared state

| Per worktree | Shared or externally shared |
|---|---|
| working directory | Git object database |
| index | most refs under `refs/` |
| `HEAD` | remotes and repository-level config |
| selected per-worktree refs/config | hooks unless configured otherwise |
| in-progress operation state | ports, databases, caches, services, deploy targets |

Some refs such as `refs/bisect`, `refs/worktree`, and `refs/rewritten` are per-worktree. Use `git rev-parse --git-path <name>` instead of assuming physical paths inside `.git`.

## Same-branch safeguard

Git normally refuses to check out one branch in multiple worktrees. `--force` can bypass the safeguard, but both checkouts would advance the same branch ref; concurrent writers can invalidate each other's assumptions. Prefer a separate branch per mutation stream.

## Script-safe inventory

```bash
git worktree list
git worktree list --verbose
git worktree list --porcelain -z   # stable machine-readable form
```

The `-z` form avoids ambiguous path/newline parsing. Tools should use it instead of scraping the human table.

## Add deliberately

```bash
git worktree add -b feat/auth ../auth-wt main
git worktree add ../existing existing-branch
git worktree add --detach ../review HEAD
```

Name the base/commit explicitly for automation. A detached worktree is useful for read-only review or testing, not as an unnamed durable change stream.

With `dev`, prefer:

```bash
dev wt create feat/auth --base main
```

This applies placement and provisioning policy before opening the runtime.

## Lock unavailable worktrees

A worktree on a removable disk or temporarily unavailable network share can be protected from prune:

```bash
git worktree lock --reason "external SSD" /path/to/worktree
git worktree unlock /path/to/worktree
```

A lock is an administrative safety signal, not file locking between writers.

## Remove versus prune

Remove a clean linked worktree through Git:

```bash
git worktree remove /path/to/worktree
```

Git refuses modified/untracked work or submodule cases unless forced. Inspect `git status --short` and preserve commits/files before considering force.

Prune only stale administrative entries whose directories disappeared:

```bash
git worktree prune --dry-run --verbose
git worktree prune
```

`prune` is not a replacement for `remove`; preview first. Locked entries are protected.

`dev wt rm` preserves the branch and includes dirty-worktree refusal; `dev sweep` can report stale recorded paths.

## Move and repair

Use Git-aware movement when supported:

```bash
git worktree move /old/path /new/path
```

If a main or linked worktree was moved manually, repair administrative links from the repository or specify new paths:

```bash
git worktree repair /new/path/feature
```

Main worktrees and worktrees with submodules have additional move restrictions. Read the installed Git version's manual before forcing a locked or unusual layout.

## Recovery checklist

1. Stop all writers and record each checkout's `git status --short` and branch.
2. List worktrees with `--porcelain -z`; do not infer registration from directories alone.
3. Preserve dirty/untracked files and create backup refs for uncertain commits.
4. If only the directory moved, use `repair`; if it vanished, preview `prune`.
5. If a branch was deleted, inspect `git reflog` and create a recovery branch before cleanup expires.
6. If a rebase is active, choose `--continue`, `--abort`, `--skip`, or `--quit` deliberately; do not remove its worktree mid-operation.
7. Verify remote reachability before calling a checkout disposable.

## Squash and `[gone]` traps

A branch can be fully represented by a squash-merged patch without satisfying ancestry checks. An upstream marked `[gone]` can also be unmerged. Confirm the pull request/patch and keep a recovery ref before deleting either branch or worktree.

## Sources

- [Git worktree documentation](https://git-scm.com/docs/git-worktree)
- [Git reflog documentation](https://git-scm.com/docs/git-reflog)
- [`internal/gitx/worktree.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/worktree.go)
- [`internal/wt/manager.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/manager.go)
