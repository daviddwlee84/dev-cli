# Worktree ownership

For ordinary clone/worktree acquisition, submodule initialization is core Git acquisition, before provisioning/runtime, and defaults to recursive pinned checkouts. `--no-provision` does not skip it; `--submodules=none` does. Gitlink children are independent submodule clones, not nested managed linked worktrees. Read `submodules.md` for selection and explicit disposal rules.

Read this before creating a worktree, sharing a checkout between agents, or when
a new checkout lacks dependencies, env files, or launcher backend state.

## The rule

| Change boundary | Owner | Where | Lifetime |
|---|---|---|---|
| Durable feature/fix/experiment/handoff | **`dev`** | `paths.worktree_path` | until external `dev work retire` |
| Harness-owned turn-scoped isolation | Claude Code | `.claude/worktrees/` | managed by that harness; do not assume artifact relocation |
| Runtime workspace/panes | Herdr | per-host runtime | until explicitly closed |
| Rendered agent history | SpecStory | process launch checkout | until committed/removed with that checkout |

Use a `dev` worktree for independent approaches or any change stream whose code,
history, or plan must remain reviewable. Agents with explicitly disjoint file
ownership may share one checkout, but `--allow-shared-checkout` is a deliberate
coordination override—not a default.

`EnterWorktree` changes an agent's working location; it does not prove an
existing SpecStory wrapper/watcher rebound its source/output paths. Start the
new process from the target worktree root.

## Create managed work or surface an existing checkout

Task tracking is optional. `dev git worktree create <branch> --base <ref>`
creates/provisions/opens a checkout without a task record. Existing Git work
survives closing its runtime without adoption. Use `dev work start` only when
the recorded start/park/resume lifecycle is wanted.

For task-free merged cleanup, run from the canonical checkout and outside the
target runtime: `dev work sweep --merged-worktrees --base main --delete-branches`
reports the exact contained candidates; after review, add `--apply` to confirm
each removal. An open PR, agent `done`, or td issue closure is not containment.
Dirty products and unsettled artifacts remain blockers. A transcript path is
not a discard allowlist; inspect its exact final changes after the writer exits.
Squash integration may require a separate manual decision.

Prefer `dev work start <repo> --task '<task>' --base '<committed-ref>'` for new
managed work: it creates/selects the checkout, provisions it, opens the runtime,
and records task intent. For an already registered external worktree that only
needs runtime visibility:

```bash
dev git worktree open <branch> --repo <repo> --runtime herdr --no-focus
```

This opens or reuses the exact checkout without switching/attaching, launching an
agent, provisioning, or adopting a task. Dirty files and the index stay untouched.
It reports branch, actual path and backend/handle immediately, followed by the
observed runtime opened/reused surface; this is not Git worktree creation. With
runtime `none`, no runtime opens and no shell `cd` handoff is emitted. Without
`--no-focus`, normal activation or shell navigation is unchanged. Runtime errors
remain errors; do not turn a failed open into a success report.

When handing work back, report the repository, branch, actual checkout path and
runtime handle/result immediately. Adoption is a separate lifecycle decision,
not a visibility switch. See `runtime-herdr.md` for reuse/fallback boundaries.

## PR/MR trial checkouts

`dev pr checkout <URL>` selects an exact existing checkout without resetting
local work, or fetches the verified request head into a task-free worktree.
Do not match a fork's branch by name alone. Multiple clone/checkout matches
require an explicit selection. Existing SSH fetch transport remains native;
`--source-remote` resolves ambiguous base remotes.

PR acquisition does not run ordinary provisioning or submodule initialization
by default. Only explicit `--provision` authorizes project setup under existing
trust rules. Without a local repository, `--try` creates an independent
catalog-backed dated clone; `--clone [--path PATH]` creates a project clone and
worktree. Neither implicitly forks or creates task intent. `--dry-run` is
read-only and `--no-open`/`--json` suppress runtime opening. Merge and optional
base synchronization never replace retirement/cleanup proof. Read
[pull requests](pull-requests.md) for exact-head merge guards and unknown receipts.

## Why dev creates the checkout

`dev` runs `git worktree add`, then asks Herdr to run
`worktree open --path <path> --no-focus`. Path policy therefore remains stable
on machines without Herdr, while Herdr still displays repo/branch provenance.
A fallback plain workspace is reported as such and is not an exact agent launch
target.

Never nest a durable worktree inside another checkout. Every watcher, language
server, backup tool and search then sees a second copy of the repository.

Herdr's native **New worktree** action is still valid for an external,
unmanaged checkout. When the canonical repository is under a configured scan
root, Git registration lets dev discover the checkout without scanning
`~/.herdr/worktrees`; dev does not auto-adopt, relocate, or provision it. Run
`dev git worktree provision <path>` if it needs the project environment and `dev work adopt`
followed by `dev work adopt --apply` only when it should enter the durable task
lifecycle. For one exact checkout, `dev repo flow [repo]` offers plan-first
metadata-only Adopt and clean branch-preserving Remove Checkout. It never removes
the canonical checkout or a harness/conflicting/locked/prunable/task-claimed row,
and it does not prune repository-wide stale registrations.

## Paths

Default:

```text
~/Worktrees/<repo>/<branch-slug>
```

Configurable through `paths.worktree_root` and `paths.worktree_path`, with
`worktree_root`, `repo`, `repo_path`, `branch`, `category`, `host`, and `date`
variables plus `slug`, `lower`, and `base` filters.

Always pass `--base` for unattended creation so the intended starting point is
explicit. `dev work start` and `dev git worktree create` otherwise resolve the repository's
default branch; they do not use whichever feature happens to be checked out.

## Provisioning

A worktree is a clean checkout. `dev git worktree create` and `dev work start` build and apply
an inspectable plan:

```bash
dev git worktree plan
dev git worktree plan --write
dev git worktree provision --dry-run
```

Effective settings come from global config or committed repo
`.dev-cli/config.toml` (legacy `.dev.toml` remains readable):

```toml
[worktree]
include = [".env", ".env.local"]
link = []
post_create = "auto"
strategy = "reinstall"
```

An executable `post_create` from `.dev-cli/config.toml` requires approval of its
exact content hash with `dev self config trust <repo> --yes`. Legacy `.dev.toml`
retains its compatibility behavior.

Only paths that are both explicitly included and genuinely gitignored are
copied. Tracked files already have the branch-correct version. Included files
must remain the same regular file from validation through open; source swaps and
symlinked destination parents are refused. Existing destinations are reported
as skipped, never falsely as copied, and file contents are never logged.

This is local provisioning only. Off-machine `dev fleet files` deliberately uses
a separate `[local_files].include` allowlist plus explicit `--to`; never infer
export permission from `[worktree].include`.

### Project-local Claude backend state

`.claude/settings.local.json` stays gitignored and is **not** a universal default
include. The verified `claude-copilot-once` wrapper preserves an existing
Copilot pin and creates/removes only its own pin when one was absent;
`codex-copilot-once` injects its backend via CLI. Neither needs a copied project
file. Claude's Copilot proxy must already be running; the Codex path may
self-start its proxy flow.

An explicitly selected sticky/plain-Claude profile may opt in exactly:

```toml
[worktree]
include = [".env", ".env.local", ".claude/settings.local.json"]
```

Verify the file arrived before launch. If absent, stop instead of allowing the
launcher to fall back silently to another backend. `dev` logs the relative path
only, never its contents.

## Dependency strategies

- `reinstall`: safe default; run lockfile-derived installer.
- `copy`: duplicate path-independent dependency trees.
- `link`: explicit and usually unsafe for mutable dependencies.
- `skip`: for external/container-managed environments.

`dev` refuses unsound choices, such as copied/linked Python virtualenvs, and
reports missing tools or failed setup without deleting the usable checkout.

## Cleanup

- `dev git worktree rm <branch>` removes only the checkout after external runtime safety checks; branch survives.
- `dev work park` records WARM; when called from its own runtime it leaves that runtime alive for normal exit.
- `dev work park --cold --push` closes eligible runtime state and removes the pushed worktree only from outside.
- `dev work done --ff` integrates and records MERGED; runtime/worktree/branch survive.
- `dev work done --pr` leaves everything active for review.
- `dev work retire` is the only complete close/wait/remove/reap path.
- `dev work sweep` reports first; `--apply` routes cleanup through retire.
- From canonical main, `dev work sweep --merged-worktrees` also audits unmanaged
  linked worktrees whose branches are contained in main. Agents must present
  that report for user confirmation before applying; branches are retained
  unless `--delete-branches` was separately approved.
- From any canonical non-bare checkout, `dev work sweep --ephemeral-worktrees` audits
  Claude Workflow isolation through bounded provider metadata plus fresh Git,
  task, artifact, caller, and every available runtime. The path/branch naming
  pattern is only a candidate label and never deletion proof. Claude Code 2.1.259
  exposes no branch/HEAD/non-replayable registration identity, so its current
  claims remain unknown/report-only rather than authorizing a reused path.
- Ephemeral apply is TTY/per-item only and revalidates a stable fingerprint under
  a common-dir lock before plain non-force removal. It never closes runtimes,
  prunes, deletes provider metadata, or repairs dirty/ignored work. Branches stay
  by default; deletion needs separately approved `--delete-branches --base REF`
  and unchanged containment/zero-unique proof before `git branch -d`.

Linked-worktree removal with gitlinks still requires `--recursive`. A child path
that is absent or truly empty, with no retained Git store and no ownership or
observation blockers, needs no initialization or remote proof merely for removal.
Deinitialized data and orphan stores are not empty. Exact reviewed empty modules
admin directories may be pruned under locks with native directory-only guards;
empty checkout directories stay for ordinary non-force Git removal. Real child
stores retain full recovery proofs, journals and rollback. Never replace a
refusal with recursive file deletion. See `submodules.md`.

Bare `dev work done` on a TTY classifies dirty content against the base before
offering commit-all or discard-all; unique discard requires `DROP`. Dirty
checkout removal may require explicit force, but caller/runtime safety is never
bypassable. Herdr `done` is not a cleanup signal, and `--cold --keep-session` is
rejected. Never raw-force-remove an agent's cwd.

When unrelated dirty bytes block the canonical fast-forward target, the same
wizard may use exact stash+restore for ordinary staged, unstaged, and untracked
work. Dirty submodules and nested repositories require separate preservation;
restore conflicts keep the exact stash and the task rather than claiming DONE.

## Triage cleanup batches

`dev triage` adds a strict ignored-data guard to its reviewed linked-checkout
cleanup. All ignored paths block by default. The operator may declare exact
clone-bound disposable relative directories; previews expose the affected paths
and sizes and removal requires the displayed CLEAN token. Nested repositories,
submodules, harness claims, unknown occupancy and canonical checkouts stay
protected. The extra guard runs inside the shared repository lock and directly
before removal. Branches and common directories are retained. See
[local triage](local-triage.md).
