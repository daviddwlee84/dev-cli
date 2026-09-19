# Worktrees

Submodule workspaces initialize recursively at committed gitlinks by default.
Use `--submodules=none` to skip, or `start --submodule PATH` to select children
for task branches. `dev submodule status/init/develop/recover` inspects, prepares
and restores child checkouts. Explicit `--recursive` cleanup verifies initialized
child clones' remote recovery before removing them and the outer linked worktree;
canonical repositories and the outer branch are retained.

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
| Feature, fix, experiment, handoff | `dev` | `~/Worktrees/<repo>/<slug>` | until external retirement |
| Harness-owned turn-scoped isolation | Claude Code | `.claude/worktrees/` | owned by that harness; no transcript-relocation guarantee |

**Might you review or return to the code/history/plan later → `dev`.**

Never put a long-lived worktree inside the repository. Every file watcher,
language server, indexer and `rg` run in the outer repo then sees a second copy
of the whole tree.

## Commands

```bash
dev start --task auth --branch feat/auth --base main  # new managed work
dev wt create feat/auth --base main   # create/provision; no task record
dev wt list                           # every worktree of this repo
dev wt open feat/auth                 # open and activate an existing one
dev wt open feat/auth --no-focus      # make it visible without navigation
dev wt rm feat/auth                   # remove the checkout; branch survives
dev wt provision                      # re-run setup on an existing checkout
```

Prefer `dev start` with an explicit base for new managed work. For an existing
external worktree that only needs runtime visibility, use
`dev wt open <branch> --repo <repo> --runtime herdr --no-focus`. It reports the
branch, actual path and backend/handle immediately, then the runtime surface
opened/reused, without switching focus or attaching. No task adoption,
provisioning, agent launch, or Git/index/dirty-file mutation occurs. Runtime
`none` reports no runtime and emits no shell `cd` handoff. Errors remain errors;
without the flag, normal activation and shell navigation are unchanged.

Report the repository, branch, actual path and runtime handle/result when handing
work back. Making an external checkout visible does not make it a managed task.

## Always pass a base

```bash
dev wt create fix/orderbook --base main
```

Without `--base`, dev uses the repository's default branch. Passing it
explicitly is still preferred for scripts and agent-driven creation because
the intended committed starting point stays reviewable in the command.

## Worktrees created directly in Herdr

Herdr's **New worktree** action is allowed. Git registers that checkout with
the canonical repository, so dev can display it as an external worktree as
long as the canonical repo is discoverable through `paths.scan_roots` or
`paths.repo_paths`—even
when the checkout lives under `~/.herdr/worktrees` and that directory is not a
scan root.

It remains deliberately unmanaged: dev does not silently create a task, move
the checkout, or run provisioning. If the work becomes durable, opt in:

```bash
dev wt provision /path/to/worktree  # optional: env files + dependencies
dev adopt                           # report candidates; changes nothing
dev adopt --apply                   # record the selected candidates as tasks
```

Until adoption, `dev park`, `dev resume`, and `dev done` have no task lifecycle
to operate on. Use `dev start` when that lifecycle is wanted from the outset.

`dev flow [repo]` also shows every registered checkout. An eligible unmanaged
linked row offers plan-first **Adopt** (task metadata only; bytes stay untouched)
and **Remove Checkout** (clean, non-force, branch always preserved). Canonical,
harness, locked/prunable, task-claimed, or ambiguous rows have no destructive
path. Flow does not prune repository-wide stale registrations; inspect and repair
those explicitly.

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

Gitlinks still need `--recursive`, even if never initialized. For linked-worktree
removal only, an absent or truly empty child path with no retained Git store may
be proved empty locally; no initialization, download, remote proof or push is
needed merely for removal. Retained/deinitialized data, orphan stores, local or
ignored files, unexpected `.git`, symlinks/reparse points, external claims and
incomplete observations block. Path-based task/artifact claims protect empty
children without inheriting parent Git identity; any non-discarded matching
artifact intent blocks, and discarded records still bind the reviewed authority.

Physical emptiness alone is not removal permission. Manually deleting a tracked
gitlink directory makes the parent dirty, blocking ordinary non-force removal
before admin pruning. Cleanup never silently creates or restores placeholders to
bypass this guard. A never-initialized, Git-created empty directory can pass;
an absent child alone does not prove the parent safe to remove.

Only exact reviewed empty modules admin scaffolding may be pruned under locks,
revalidating directory identity and emptiness immediately before native
directory-only removal. Empty checkout directories stay for normal non-force Git
removal. Real child stores retain full recovery proofs and journals/rollback;
partial pruning failure is not retirement success. Layout changes, new child
initialization or claims require a new plan. General removal guards and
initialization/publication policy are unchanged; no recursive file-deletion
workaround is used. See `dev help retirement` and `dev help ai-artifacts`.

If a directory was deleted behind Git's back, the registration is a recovery
case. Inspect repository-wide `git worktree prune` scope before applying it;
`dev flow` reports the drift and does not prune implicitly.
