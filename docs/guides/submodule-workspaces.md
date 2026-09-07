---
description: Initialize complete submodule workspaces, select task branches, and retire child repositories before their parent.
authority: project
status: evolving
verified_on: 2026-09-07
tested_with: Git 2.55.0
---

# Submodule workspaces

A superproject records commit IDs for its submodules. A complete workspace
contains the outer worktree and an independent checkout of each child repository.
It does not copy uncommitted work from the original checkout.

## Add a repository as a submodule

```bash
dev submodule add                           # source/path/mode wizard
dev submodule add owner/library libs/library --dry-run --json
dev submodule add owner/library libs/library --checkout=pinned --ref=v1.2.0 --yes
dev repo add-as-submodule owner/library libs/library --checkout=default-branch --yes
```

`source` may be an exact known name, `owner/name`, or a network Git URL.
The picker merges known local remotes and the existing private forge cache;
it never refreshes providers implicitly. Ambiguous names need selection or an
exact URL. Local repositories supply a remote URL, not their working files or
local object store. Local-only sources, credential-bearing URLs and adoption of
existing directories are not supported. Use `dev repo remote --refresh` explicitly
when the cached inventory needs updating.

The parent is the nearest checkout containing cwd, including a linked worktree
or a submodule; `--parent PATH` selects another exact checkout. Paths are relative
to that checkout's root, defaulting to the source name. A path crossing an existing
child repo is rejected: select that child as the parent instead. The parent must
have a commit and be on a branch. Existing targets/stores, symlinks, conflicts,
in-progress Git operations and modified `.gitmodules` block addition; unrelated
staged/dirty work is preserved. Filters/encodings on `.gitmodules` are unsupported.

The wizard offers `pinned` and `default-branch`; non-interactive default is
`pinned`. `--ref` selects a fetched commit/tag/branch only in pinned mode;
otherwise the new clone's actual default branch determines the commit. Default-
branch mode creates a tracking branch without assuming its name is `main`.
Both modes stage a fixed gitlink. This is not an `update --remote` policy and
does not change future clone/worktree initialization or create task-member intent.
Use `dev submodule develop` separately when the child belongs to a managed task.

`--submodules=recursive|none` overrides the parent's effective initialization
policy for the new child's descendants only. Addition stages `.gitmodules` and
the new gitlink, never commits, pushes, creates a runtime, or changes other
checkouts' shared submodule configuration. Non-interactive mutation needs a source
and `--yes`; `--json` never prompts. `--dry-run` neither writes nor connects and
marks unresolved refs as pending rather than claiming a verified commit.

JSON includes `operation`, `parent`, `source`, relative `path`, `checkout`,
optional `ref`, `submodules`, `phase`, `git_dir`, optional `head`/`branch`,
`staged`, and optional `warnings`. Phases distinguish `planned`, `not-added`,
`clone-incomplete`, `cloned`, `added-unstaged`, `added`,
`initialization-incomplete`, and `complete`; failures exit nonzero while still
reporting any partial result. A failed clone/ref/staging step may retain files
and metadata. Inspect the exact paths, finish metadata/staging manually where
needed, and use `dev submodule init` inside the new child to retry missing
descendants. Do not rerun add over existing paths, force-delete partial clones,
or use the retirement-only `recover` command for an interrupted addition.

REPOS/REMOTE `y u` copies a network clone URL; REPOS uses the selected checkout's
origin (or asks among remaining remotes), REMOTE uses CloneURL then SSHURL.
It never copies the browser URL as a substitute or fetches to resolve one.

## Initialize and develop

```bash
dev repo clone owner/dotfiles-all
dev start dotfiles-all --task platform-change --base main \
  --submodule dotfiles --submodule dotfiles-windows
# In the new managed worktree:
dev submodule status
dev submodule develop another/module --submodule-base another/module=origin/main
```

Clone and worktree creation initialize recursively by default. Each child starts
at the parent's gitlink in detached HEAD; that is the reproducible starting
point, not an error. Selected children get the outer task's branch name at that
same commit. Existing branches are not overwritten or silently adopted. Direct
and branch-only task modes do not automatically switch child branches.

```toml
[submodules]
init = "recursive" # or "none"
develop = ["dotfiles", "dotfiles-windows"]
```

The global config and `.dev-cli/config.toml` support these settings. Explicit
flags/wizard choices override project settings, which override global settings.
The clone's project settings are read after acquiring the outer repository.
`--submodules=none` skips initialization. `--no-provision` skips dependency setup,
not Git initialization. No child is automatically advanced to the latest main.

`dev submodule init --dry-run` inspects locally. Without `--dry-run`, it fills
missing checkouts and may contact their configured sources. It preserves existing
checkout HEADs and dirty content; a nonempty uninitialized directory is blocked.
Initialization failure retains partial results, reports a failure, and prevents
runtime handoff/agent dispatch. Retry initialization before continuing setup.

## Integrate from the inside out

For a cross-platform change, commit, integrate and push each affected child
first. Then stage the matching gitlinks together and commit the outer change.
For `dotfiles-all`, a change affecting both platforms gets one outer pointer
commit containing both gitlinks and matching cross-repository documentation.

The manager verifies prerequisites; it does not automatically commit, push or
merge child work. Outer `--push` is not a recursive child push. Gitlink updates
and conflict resolution remain explicit.

`dev status`, `dev repo context`, `dev ls --json`, and repository UI evidence
include submodule state. `dev submodule status --json` exposes the complete local
graph. Uninitialized, unavailable and failed observations never mean clean.
These reads do not fetch or query remotes.

## Park and retire

Run cleanup outside the target checkout and its runtime:

```bash
dev park platform-change --cold --push --recursive
dev resume platform-change --fetch
# After the outer task has been integrated:
dev retire platform-change --recursive
dev sweep --merged-worktrees --recursive # report first
```

Cold requires committed, pushed, reconstructible work, but not integration.
Retire additionally proves selected children are integrated into their recorded
base. Workspace-member intent is versioned separately from task TOML and is
used to reconstruct selected branches at the gitlinks when resuming.

`--recursive` authorizes disposal of the workspace's independent child clones,
including private refs and objects. The outer branch stays unless separately
requested; canonical and shared repositories are never disposed. Omitting the
flag blocks parent removal when child repositories remain. `--force` cannot
bypass child recovery checks. `dev flow` offers explicit recursive managed and
unmanaged checkout actions; the done cleanup wizard asks about child disposal.

Before mutation, each child is compared with fresh refs from its declared
recovery origin using an isolated fetch. A configured origin or ahead=0 alone
is not proof. Unpublished refs, local-only reflog/unreachable objects, stash,
dirty/untracked/ignored content, unfinished artifacts, other task claims,
runtime activity, and additional child worktrees block cleanup. Filtered/LFS
data, sparse/partial layouts and private Git configuration or files without a
complete preservation proof also block. Initialize missing children before
requesting a complete recursive recovery proof.

The child origin must match its declared source. Remote failure or changing refs
halts cleanup. Raw Git/external writers and later remote history deletion remain
outside dev's cooperative locking and run-local proof guarantees.

## Interrupted cleanup

Children are staged from deepest to shallowest into a private temporary area on
the same filesystem. Empty gitlink directories remain until ordinary non-force
outer removal succeeds. The workflow does not use `submodule deinit` to rewrite
shared superproject settings.

Each move has a synced journal. A failed outer removal restores original child
paths when they remain unclaimed. An interruption or reused path retains the
quarantine and reports its exact `journal.json`:

```bash
dev submodule recover /exact/.dev-submodule-retirement-ID/journal.json --dry-run
dev submodule recover /exact/.dev-submodule-retirement-ID/journal.json
```

Recovery restores rather than reusing an old proof to delete data. A removed
outer checkout can be reconstructed only when its retained branch still matches
the recorded commit. Reused or nonempty restore paths block restoration. Rerun
the ordinary recursive cleanup after recovery for a fresh plan and remote proof.

Refresh shell integration after upgrading to use recursive post-done handoff.
Older wrappers reject the new recursive action instead of guessing its meaning.
