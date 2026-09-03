# Worktree ownership

Read this before creating a worktree, sharing a checkout between agents, or when
a new checkout lacks dependencies, env files, or launcher backend state.

## The rule

| Change boundary | Owner | Where | Lifetime |
|---|---|---|---|
| Durable feature/fix/experiment/handoff | **`dev`** | `paths.worktree_path` | until external `dev retire` |
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
`dev wt provision <path>` if it needs the project environment and `dev adopt`
followed by `dev adopt --apply` only when it should enter the durable task
lifecycle. For one exact checkout, `dev flow [repo]` offers plan-first
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

Always pass `--base` for unattended creation. A new branch otherwise inherits
the current HEAD, which may be an unrelated feature.

## Provisioning

A worktree is a clean checkout. `dev wt create` and `dev start` build and apply
an inspectable plan:

```bash
dev wt plan
dev wt plan --write
dev wt provision --dry-run
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
exact content hash with `dev config trust <repo> --yes`. Legacy `.dev.toml`
retains its compatibility behavior.

Only paths that are both explicitly included and genuinely gitignored are
copied. Tracked files already have the branch-correct version. Included files
must remain the same regular file from validation through open; source swaps and
symlinked destination parents are refused. Existing destinations are reported
as skipped, never falsely as copied, and file contents are never logged.

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

- `dev wt rm <branch>` removes only the checkout after external runtime safety checks; branch survives.
- `dev park` records WARM; when called from its own runtime it leaves that runtime alive for normal exit.
- `dev park --cold --push` closes eligible runtime state and removes the pushed worktree only from outside.
- `dev done --ff` integrates and records MERGED; runtime/worktree/branch survive.
- `dev done --pr` leaves everything active for review.
- `dev retire` is the only complete close/wait/remove/reap path.
- `dev sweep` reports first; `--apply` routes cleanup through retire.
- From canonical main, `dev sweep --merged-worktrees` also audits unmanaged
  linked worktrees whose branches are contained in main. Agents must present
  that report for user confirmation before applying; branches are retained
  unless `--delete-branches` was separately approved.

Bare `dev done` on a TTY classifies dirty content against the base before
offering commit-all or discard-all; unique discard requires `DROP`. Dirty
checkout removal may require explicit force, but caller/runtime safety is never
bypassable. Herdr `done` is not a cleanup signal, and `--cold --keep-session` is
rejected. Never raw-force-remove an agent's cwd.
