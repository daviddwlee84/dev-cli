---
description: Choose the owner and location of each worktree, then provision ignored files and dependencies safely.
authority: project
status: stable
verified_on: 2026-08-29
---

# Worktrees and provisioning

Submodule Git initialization precedes environment provisioning and defaults to recursive pinned checkouts. `--no-provision` does not disable it; use `--submodules=none`. Initialization failure retains the worktree without opening its runtime. See [Submodule workspaces](submodule-workspaces.md).

A Git worktree begins as a clean checkout. `dev` owns durable change-stream worktrees and builds an inspectable plan for the ignored files and dependencies needed to make them usable.

## Choose one lifecycle owner

| Worktree kind | Owner | Typical location | Lifetime |
|---|---|---|---|
| feature, fix, experiment, cross-machine handoff | `dev` | configured `paths.worktree_path` | `dev work done` records MERGED only; a later `dev work retire`, explicit `dev git worktree rm`, or approved sweep removes it |
| harness-scoped isolation | Claude Code or another harness | harness-owned directory such as `.claude/worktrees/<name>/` | governed by that harness's retention and safe-cleanup rules; Flow never adopts or removes it |
| externally created linked worktree | external until adopted | tool-specific | visible to `dev`/`dev repo flow`; unmanaged until explicit adoption |

Use `dev` when code, history, or plans must remain reviewable or a human may return later. Do not nest a long-lived `dev` worktree inside a repository; file watchers, language servers, backup tools, and searches in the outer checkout would see a second copy of the tree.

`dev` creates the checkout with Git at its configured path. Herdr only opens that existing path, so worktree placement remains identical on machines without Herdr.

`dev repo flow [repo]` uses Git's authoritative worktree records to show canonical,
managed, unmanaged, and strict `.claude/worktrees/` harness rows, plus task-only
rows that have no checkout. It never guesses ownership from a `worktree-*`
branch prefix; ambiguous path/task binding is labelled CONFLICT and stops
lifecycle mutation. See [Repository lifecycle flow](repository-flow.md).

## New managed work versus existing checkout visibility

Prefer `dev work start` with an explicit committed base for new managed work:

```bash
dev work start api --task "auth fix" --branch fix/auth --base main
```

For an existing registered external worktree that only needs runtime visibility:

```bash
dev git worktree open fix/auth --repo api --runtime herdr --no-focus
```

The open command reports branch, actual path and backend/handle immediately, then
the actual runtime opened/reused surface. It does not switch focus or attach,
create another Git worktree, adopt a task, provision, launch an agent, or change
Git refs, the index or dirty files. A fallback workspace is reported as a workspace,
not a new Git checkout or an agent launch target. With runtime `none`, it opens no
runtime and emits no shell `cd` handoff. Without the flag, activation/shell
navigation is unchanged; runtime errors still fail the command.

Report repository, branch, actual checkout path and runtime handle/result when
handing work back. Adoption is separate task intent, not a visibility switch;
harness-owned temporary isolation remains distinct from durable implementation
work. See [Parallel agents and runtimes](parallel-agents-runtimes.md).

## Inspect before creating

```bash
dev git worktree plan
dev git worktree plan --write          # seed repository-owned .dev-cli/config.toml
dev git worktree create feat/auth --base main
dev git worktree list
```

`dev git worktree plan` reads lockfiles, tool availability, configured include/link rules, and Git ignore state without changing the checkout. The resulting plan shows every runnable or skipped step and any safety downgrade.

## Carry ignored files by allowlist

```toml
[worktree]
include = [".env", ".env.local", "config/local.json"]
link = []
post_create = "auto"
strategy = "reinstall"
```

Only paths that are both listed and confirmed by Git as ignored are copied. A tracked file already arrives through the branch; copying a different checkout's version over it would violate branch isolation.

Included files are copied as files, not logged. Current provisioning also rechecks the source and destination path shape while opening/copying so source swaps and symlinked destination parents are refused.

Do not add `.claude/settings.local.json` globally. Include that exact file only for a deliberately chosen launcher whose behavior depends on it, and verify the copy in the plan.

## Select dependency strategy by correctness

| Strategy | Effect | Guidance |
|---|---|---|
| `reinstall` | run the lockfile-derived install command | safe default |
| `copy` | duplicate an installed dependency directory | use only where paths are portable |
| `link` | share one dependency directory | fastest and usually unsafe for concurrent writers |
| `skip` | leave dependencies absent | container- or CI-driven development |

Per-ecosystem overrides live in the project or global config:

```toml
[worktree.strategies]
node = "copy"
```

New project-owned overrides belong in `.dev-cli/config.toml`; legacy
`.dev.toml` remains readable under its compatibility behavior. A `post_create`
command from `.dev-cli/config.toml` does not run until its exact
executable-config hash is approved with `dev self config trust . --yes`. Changing
the command invalidates that approval.

Important built-in decisions:

- Python virtual environments cannot be copied or linked because they embed absolute paths; `uv sync` can reuse its global cache.
- Node `node_modules` may be copied, but sharing is refused because either checkout can mutate it.
- Go uses a global content-addressed module cache, so there is no checkout-local dependency tree to copy.
- Cargo `target/` may be copied, but sharing concurrent build output is refused.

An invalid or unsound request narrows to `reinstall` with a warning instead of silently creating a broken environment.

## Auto-detected setup

`post_create = "auto"` recognizes one manager per ecosystem, in priority order:

| Marker | Command |
|---|---|
| `uv.lock` | `uv sync` |
| `poetry.lock` | `poetry install` |
| `pnpm-lock.yaml` | `pnpm install --frozen-lockfile` |
| `package-lock.json` | `npm ci` |
| `yarn.lock` | `yarn install --immutable` |
| `go.mod` | `go mod download` |
| `Cargo.toml` | `cargo fetch` |
| `Gemfile.lock` | `bundle install` |

A missing tool is reported and skipped. A failed setup command leaves the worktree in place so the branch and checkout can be repaired rather than discarded.

## Reprovision or remove

```bash
dev git worktree provision /path/to/worktree --dry-run
dev git worktree provision /path/to/worktree
dev git worktree rm feat/auth
```

Removing a worktree and deleting a branch are separate decisions. `dev git worktree rm` preserves the branch and refuses a dirty checkout without explicit force. If the directory disappeared outside Git, it prunes the stale administrative entry.

## Sources

- [`internal/wt/plan.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/plan.go)
- [`internal/wt/ecosystem.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/ecosystem.go)
- [`internal/wt/provision.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/provision.go)
- [`internal/wt/manager.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/manager.go)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
